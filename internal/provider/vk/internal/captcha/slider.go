package captcha

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/jpeg" // регистрация JPEG-декодера для image.Decode
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
)

type sliderPuzzle struct {
	Image    image.Image
	Size     int
	Swaps    []int
	Attempts int
}

type sliderGuess struct {
	Index         int
	Swaps         []int
	Score         int64
	ScoreRGB      int64
	ScoreLuma     int64
	ScoreText     float64
	ConsensusRank int
}

func (s *captchaSession) solveSliderCaptcha(
	sessionToken string,
	content captchaContentRef,
) (string, error) {
	if content.Value == "" {
		return "", errors.New("slider content settings missing")
	}
	s.logger().Debugf("[Captcha] slider getContent source=%s len=%d", content.Source, len(content.Value))
	values := [][2]string{
		{"session_token", sessionToken},
		{"domain", s.domain},
		{"adFp", ""},
		{"access_token", ""},
		{"captcha_settings", content.Value},
	}

	resp, err := s.captchaRequest("captchaNotRobot.getContent", values)
	if err != nil {
		return "", fmt.Errorf("slider getContent failed: %w", err)
	}
	puzzle, err := parseSliderPuzzle(resp)
	if err != nil {
		return "", err
	}
	s.logger().Debugf("[Captcha] slider puzzle decoded: grid=%d attempts=%d swaps=%d", puzzle.Size, puzzle.Attempts, len(puzzle.Swaps))

	guesses, err := rankSliderGuesses(puzzle.Image, puzzle.Size, puzzle.Swaps)
	if err != nil {
		return "", err
	}

	limit := min(puzzle.Attempts, len(guesses))
	if limit <= 0 {
		return "", errors.New("slider has no attempts available")
	}
	attempts := pickSliderAttempts(guesses, limit)
	s.logger().Debugf("[Captcha] slider guesses ranked: total=%d limit=%d", len(guesses), limit)

	if err := s.sendComponentDone(sessionToken); err != nil {
		return "", err
	}

	for i, guess := range attempts {
		s.logger().Debugf("[Captcha] slider attempt %d/%d (guess #%d)", i+1, len(attempts), guess.Index)
		answerData, err := json.Marshal(struct {
			Value []int `json:"value"`
		}{Value: guess.Swaps})
		if err != nil {
			return "", err
		}
		// Протяжка ручки: координаты никуда не уходят, но время сессии задаёт
		// длину массивов телеметрии.
		if dragErr := s.dwell(650, 1750); dragErr != nil {
			return "", dragErr
		}

		check, err := s.performCaptchaCheck(sessionToken, string(answerData))
		if err != nil {
			return "", err
		}
		s.logger().Debugf("[Captcha] slider attempt %d status=%s show_type=%s token_len=%d", i+1, check.Status, check.ShowType, len(check.SuccessToken))
		if strings.EqualFold(check.Status, "ok") {
			if check.SuccessToken == "" {
				return "", errors.New("captcha success token not found")
			}
			s.logger().Infof("[Captcha] slider accepted on attempt %d", i+1)
			return check.SuccessToken, nil
		}
		if strings.EqualFold(check.Status, "error_limit") {
			return "", errCaptchaRateLimit
		}
		// Виджет показал ошибку - человеку нужно время, чтобы это осознать.
		if dwellErr := s.dwell(700, 1600); dwellErr != nil {
			return "", dwellErr
		}
	}
	return "", errors.New("slider guesses exhausted")
}

func parseSliderPuzzle(raw map[string]any) (*sliderPuzzle, error) {
	resp, ok := raw["response"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("invalid slider content response: %v", raw)
	}
	status := captchaStringifyAny(resp["status"])
	if !strings.EqualFold(status, "ok") {
		return nil, fmt.Errorf("slider getContent status: %s", status)
	}
	rawImage := captchaStringifyAny(resp["image"])
	if rawImage == "" {
		return nil, errors.New("slider image missing")
	}
	rawSteps, ok := resp["steps"].([]any)
	if !ok {
		return nil, errors.New("slider steps missing")
	}
	steps := make([]int, 0, len(rawSteps))
	for _, item := range rawSteps {
		switch v := item.(type) {
		case float64:
			steps = append(steps, int(v))
		case int:
			steps = append(steps, v)
		case string:
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil {
				return nil, fmt.Errorf("invalid numeric value: %v", item)
			}
			steps = append(steps, n)
		default:
			return nil, fmt.Errorf("invalid numeric value: %v", item)
		}
	}
	size, swaps, attempts, err := splitSliderSteps(steps)
	if err != nil {
		return nil, err
	}
	data, err := base64.StdEncoding.DecodeString(rawImage)
	if err != nil {
		return nil, fmt.Errorf("decode slider image: %w", err)
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode slider image: %w", err)
	}
	return &sliderPuzzle{Image: img, Size: size, Swaps: swaps, Attempts: attempts}, nil
}

func splitSliderSteps(steps []int) (int, []int, int, error) {
	if len(steps) < 3 {
		return 0, nil, 0, errors.New("slider steps payload too short")
	}
	size := steps[0]
	if size <= 0 {
		return 0, nil, 0, fmt.Errorf("invalid slider size: %d", size)
	}
	tail := append([]int(nil), steps[1:]...)
	attempts := 4
	if len(tail)%2 != 0 {
		// Штатный формат: [size, ...пары свапов, attempts].
		attempts = tail[len(tail)-1]
		tail = tail[:len(tail)-1]
		Log().Debugf("[Captcha] slider attempts from payload=%d", attempts)
	} else {
		Log().Debugf("[Captcha] slider payload without attempts counter; default=%d", attempts)
	}
	if attempts <= 0 {
		attempts = 4
	}
	if len(tail) == 0 || len(tail)%2 != 0 {
		return 0, nil, 0, errors.New("invalid slider swap payload")
	}
	return size, tail, attempts, nil
}

func rankSliderGuesses(img image.Image, gridSize int, swaps []int) ([]sliderGuess, error) {
	candidateCount := len(swaps) / 2
	if candidateCount == 0 {
		return nil, errors.New("slider has no candidates")
	}

	guesses := make([]sliderGuess, candidateCount)
	for idx := 1; idx <= candidateCount; idx++ {
		active := activeSwapsForIndex(swaps, idx)
		mapping, err := applySliderSwaps(gridSize, active)
		if err != nil {
			return nil, err
		}
		guesses[idx-1] = sliderGuess{Index: idx, Swaps: active}
		guesses[idx-1].ScoreLuma = seamScoreLuma(img, gridSize, mapping)
	}

	lumaOrder := append([]sliderGuess(nil), guesses...)
	sort.SliceStable(lumaOrder, func(i, j int) bool {
		if lumaOrder[i].ScoreLuma == lumaOrder[j].ScoreLuma {
			return lumaOrder[i].Index < lumaOrder[j].Index
		}
		return lumaOrder[i].ScoreLuma < lumaOrder[j].ScoreLuma
	})
	lumaRank := make(map[int]int, candidateCount)
	for rank, g := range lumaOrder {
		lumaRank[g.Index] = rank
	}

	// Второй проход тяжелее первого - считаем параллельно, каждый в свою ячейку.
	errs := make([]error, candidateCount)
	var wg sync.WaitGroup
	for i := range guesses {
		wg.Go(func() {
			mapping, err := applySliderSwaps(gridSize, guesses[i].Swaps)
			if err != nil {
				errs[i] = err
				return
			}
			guesses[i].ScoreRGB, guesses[i].ScoreText = seamScoreRGBText(img, gridSize, mapping)
		})
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}

	rgbOrder := append([]sliderGuess(nil), guesses...)
	sort.SliceStable(rgbOrder, func(i, j int) bool {
		if rgbOrder[i].ScoreRGB == rgbOrder[j].ScoreRGB {
			return rgbOrder[i].Index < rgbOrder[j].Index
		}
		return rgbOrder[i].ScoreRGB < rgbOrder[j].ScoreRGB
	})
	rgbRank := make(map[int]int, len(rgbOrder))
	for rank, g := range rgbOrder {
		rgbRank[g.Index] = rank
	}

	textOrder := append([]sliderGuess(nil), guesses...)
	sort.SliceStable(textOrder, func(i, j int) bool {
		if textOrder[i].ScoreText == textOrder[j].ScoreText {
			return textOrder[i].Index < textOrder[j].Index
		}
		return textOrder[i].ScoreText < textOrder[j].ScoreText
	})
	textRank := make(map[int]int, len(textOrder))
	for rank, g := range textOrder {
		textRank[g.Index] = rank
	}

	for i := range guesses {
		g := &guesses[i]
		g.ConsensusRank = lumaRank[g.Index] + rgbRank[g.Index] + textRank[g.Index]
		g.Score = int64(g.ConsensusRank)
	}

	sort.SliceStable(guesses, func(i, j int) bool {
		if guesses[i].ConsensusRank == guesses[j].ConsensusRank {
			if guesses[i].ScoreLuma == guesses[j].ScoreLuma {
				return guesses[i].Index < guesses[j].Index
			}
			return guesses[i].ScoreLuma < guesses[j].ScoreLuma
		}
		return guesses[i].ConsensusRank < guesses[j].ConsensusRank
	})
	return guesses, nil
}

// pickSliderAttempts разносит попытки по номеру кандидата: соседние отличаются
// одним свапом и промахиваются вместе.
func pickSliderAttempts(guesses []sliderGuess, limit int) []sliderGuess {
	const minGap = 3
	out := make([]sliderGuess, 0, limit)
	taken := func(index, gap int) bool {
		return slices.ContainsFunc(out, func(g sliderGuess) bool {
			return absInt(g.Index-index) < gap
		})
	}
	for _, gap := range []int{minGap, 1} {
		for _, g := range guesses {
			if len(out) == limit {
				return out
			}
			if !taken(g.Index, gap) {
				out = append(out, g)
			}
		}
	}
	return out
}

func activeSwapsForIndex(swaps []int, index int) []int {
	if index <= 0 {
		return []int{}
	}
	end := min(index*2, len(swaps))
	return append([]int(nil), swaps[:end]...)
}

func applySliderSwaps(gridSize int, swaps []int) ([]int, error) {
	tileCount := gridSize * gridSize
	if tileCount <= 0 {
		return nil, fmt.Errorf("invalid slider tile count: %d", tileCount)
	}
	if len(swaps)%2 != 0 {
		return nil, fmt.Errorf("invalid slider swaps length: %d", len(swaps))
	}
	mapping := make([]int, tileCount)
	for i := range mapping {
		mapping[i] = i
	}
	for i := 0; i < len(swaps); i += 2 {
		left := swaps[i]
		right := swaps[i+1]
		if left < 0 || right < 0 || left >= tileCount || right >= tileCount {
			return nil, fmt.Errorf("slider step out of range: %d,%d", left, right)
		}
		mapping[left], mapping[right] = mapping[right], mapping[left]
	}
	return mapping, nil
}

func seamScoreLuma(img image.Image, gridSize int, mapping []int) int64 {
	bounds := img.Bounds()
	var score int64
	for row := range gridSize {
		for col := range gridSize - 1 {
			leftIdx := row*gridSize + col
			rightIdx := leftIdx + 1
			leftDst := sliderTileRect(bounds, gridSize, leftIdx)
			rightDst := sliderTileRect(bounds, gridSize, rightIdx)
			leftSrc := sliderTileRect(bounds, gridSize, mapping[leftIdx])
			rightSrc := sliderTileRect(bounds, gridSize, mapping[rightIdx])
			for y := range min(leftDst.Dy(), rightDst.Dy()) {
				yy := leftDst.Min.Y + y
				a := sampleLumaMapped(img, leftDst, leftSrc, leftDst.Max.X-1, yy)
				b := sampleLumaMapped(img, rightDst, rightSrc, rightDst.Min.X, yy)
				score += int64(absInt(int(a) - int(b)))
			}
		}
	}
	for row := range gridSize - 1 {
		for col := range gridSize {
			topIdx := row*gridSize + col
			bottomIdx := (row+1)*gridSize + col
			topDst := sliderTileRect(bounds, gridSize, topIdx)
			bottomDst := sliderTileRect(bounds, gridSize, bottomIdx)
			topSrc := sliderTileRect(bounds, gridSize, mapping[topIdx])
			bottomSrc := sliderTileRect(bounds, gridSize, mapping[bottomIdx])
			for x := range min(topDst.Dx(), bottomDst.Dx()) {
				xx := topDst.Min.X + x
				a := sampleLumaMapped(img, topDst, topSrc, xx, topDst.Max.Y-1)
				b := sampleLumaMapped(img, bottomDst, bottomSrc, xx, bottomDst.Min.Y)
				score += int64(absInt(int(a) - int(b)))
			}
		}
	}
	return score
}

func seamScoreRGBText(img image.Image, gridSize int, mapping []int) (int64, float64) {
	bounds := img.Bounds()
	height := float64(bounds.Dy())
	textCenters := []float64{
		float64(bounds.Min.Y) + 0.2*height,
		float64(bounds.Min.Y) + 0.5*height,
		float64(bounds.Min.Y) + 0.8*height,
	}
	sigma := height * 0.14
	if sigma < 1.0 {
		sigma = 1.0
	}
	weight := func(y int) float64 {
		yf := float64(y)
		best := math.Abs(yf - textCenters[0])
		for _, center := range textCenters[1:] {
			best = min(best, math.Abs(yf-center))
		}
		return 1 + 3*math.Exp(-(best*best)/(2*sigma*sigma))
	}

	var rgbScore int64
	var textScore float64
	for row := range gridSize {
		for col := range gridSize - 1 {
			leftIdx := row*gridSize + col
			rightIdx := leftIdx + 1
			leftDst := sliderTileRect(bounds, gridSize, leftIdx)
			rightDst := sliderTileRect(bounds, gridSize, rightIdx)
			leftSrc := sliderTileRect(bounds, gridSize, mapping[leftIdx])
			rightSrc := sliderTileRect(bounds, gridSize, mapping[rightIdx])
			for y := range min(leftDst.Dy(), rightDst.Dy()) {
				yy := leftDst.Min.Y + y
				l := sampleColorMapped(img, leftDst, leftSrc, leftDst.Max.X-1, yy)
				r := sampleColorMapped(img, rightDst, rightSrc, rightDst.Min.X, yy)
				rgbScore += pixelDiff(l, r)
				_, _, lb, _ := l.RGBA()
				_, _, rb, _ := r.RGBA()
				textScore += weight(yy) * float64(absInt(int(lb>>8)-int(rb>>8)))
			}
		}
	}
	for row := range gridSize - 1 {
		for col := range gridSize {
			topIdx := row*gridSize + col
			bottomIdx := (row+1)*gridSize + col
			topDst := sliderTileRect(bounds, gridSize, topIdx)
			bottomDst := sliderTileRect(bounds, gridSize, bottomIdx)
			topSrc := sliderTileRect(bounds, gridSize, mapping[topIdx])
			bottomSrc := sliderTileRect(bounds, gridSize, mapping[bottomIdx])
			for x := range min(topDst.Dx(), bottomDst.Dx()) {
				xx := topDst.Min.X + x
				t := sampleColorMapped(img, topDst, topSrc, xx, topDst.Max.Y-1)
				b := sampleColorMapped(img, bottomDst, bottomSrc, xx, bottomDst.Min.Y)
				rgbScore += pixelDiff(t, b)
				_, _, tb, _ := t.RGBA()
				_, _, bb, _ := b.RGBA()
				textScore += 0.65 * float64(absInt(int(tb>>8)-int(bb>>8)))
			}
		}
	}
	return rgbScore, textScore
}

func sampleColorMapped(img image.Image, dstRect, srcRect image.Rectangle, dstX, dstY int) color.Color {
	dx := max(dstRect.Dx(), 1)
	dy := max(dstRect.Dy(), 1)
	sx := srcRect.Min.X + (dstX-dstRect.Min.X)*srcRect.Dx()/dx
	sy := srcRect.Min.Y + (dstY-dstRect.Min.Y)*srcRect.Dy()/dy
	return img.At(sx, sy)
}

func sampleLumaMapped(img image.Image, dstRect, srcRect image.Rectangle, dstX, dstY int) uint8 {
	c := sampleColorMapped(img, dstRect, srcRect, dstX, dstY)
	r, g, b, _ := c.RGBA()
	y := min((299*(r>>8)+587*(g>>8)+114*(b>>8))/1000, 255)
	return uint8(y) //nolint:gosec // bounded above by 255
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func sliderTileRect(bounds image.Rectangle, gridSize, index int) image.Rectangle {
	row := index / gridSize
	col := index % gridSize
	x0 := bounds.Min.X + col*bounds.Dx()/gridSize
	x1 := bounds.Min.X + (col+1)*bounds.Dx()/gridSize
	y0 := bounds.Min.Y + row*bounds.Dy()/gridSize
	y1 := bounds.Min.Y + (row+1)*bounds.Dy()/gridSize
	return image.Rect(x0, y0, x1, y1)
}

func pixelDiff(left color.Color, right color.Color) int64 {
	lr, lg, lb, _ := left.RGBA()
	rr, rg, rb, _ := right.RGBA()
	return absDiff(lr, rr) + absDiff(lg, rg) + absDiff(lb, rb)
}

func absDiff(left uint32, right uint32) int64 {
	if left > right {
		return int64(left - right)
	}
	return int64(right - left)
}
