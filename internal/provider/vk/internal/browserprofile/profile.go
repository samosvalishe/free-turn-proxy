package browserprofile

import (
	"encoding/json"
	"os"

	fhttp "github.com/bogdanfinn/fhttp"
)

type Profile struct {
	UserAgent       string
	SecChUa         string
	SecChUaMobile   string
	SecChUaPlatform string
}

type Saved struct {
	Profile
	DeviceJSON string
	BrowserFp  string
}

const profileFile = "vk_profile.json"

func Load() (*Saved, error) {
	data, err := os.ReadFile(profileFile)
	if err != nil {
		return nil, err
	}
	var sp Saved
	if err := json.Unmarshal(data, &sp); err != nil {
		return nil, err
	}
	return &sp, nil
}

func Save(sp Saved) error {
	data, err := json.MarshalIndent(sp, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(profileFile, data, 0600)
}

func ApplyFhttp(req *fhttp.Request, profile Profile) {
	req.Header.Set("User-Agent", profile.UserAgent)
	req.Header.Set("sec-ch-ua", profile.SecChUa)
	req.Header.Set("sec-ch-ua-mobile", profile.SecChUaMobile)
	req.Header.Set("sec-ch-ua-platform", profile.SecChUaPlatform)
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("DNT", "1")
}
