package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
)

func main() {
	type powPayload struct {
		Hash       string `json:"hash"`
		Nonce      int    `json:"nonce"`
		DurationMs int    `json:"duration_ms"`
	}

	payload := powPayload{
		Hash:       "006730e6f214ac8707eaf838e3f126b6b1ed633c648563434a974570090741d5",
		Nonce:      20,
		DurationMs: 2,
	}

	raw, _ := json.Marshal(payload)
	b64 := base64.RawURLEncoding.EncodeToString(raw)
	fmt.Println("v2." + b64)
	
	userStr := "eyJoYXNoIjoiMDA2NzMwZTZmMjE0YWM4NzA3ZWFmODM4ZTNmMTI2YjZiMWVkNjMzYzY0ODU2MzQzNGE5NzQ1NzAwOTA3NDFkNSIsIm5vbmNlIjoyMCwiZHVyYXRpb25fbXMiOjJ9"
	
	decoded, _ := base64.RawURLEncoding.DecodeString(userStr)
	fmt.Printf("Decoded User: %s\n", string(decoded))
	
	decodedStd, err := base64.StdEncoding.DecodeString(userStr)
	fmt.Printf("Decoded Std: %s, Err: %v\n", string(decodedStd), err)
	
	decodedRawStd, err := base64.RawStdEncoding.DecodeString(userStr)
	fmt.Printf("Decoded RawStd: %s, Err: %v\n", string(decodedRawStd), err)
}
