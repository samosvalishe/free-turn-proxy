package vkauth

import "testing"

func TestCaptchaSolveModeForAttempt(t *testing.T) {
	t.Parallel()

	t.Run("default flow", func(t *testing.T) {
		t.Parallel()

		mode, ok := CaptchaSolveModeForAttempt(0, false)
		if !ok || mode != CaptchaSolveModeAuto {
			t.Fatalf("expected first attempt to use auto captcha, got mode=%v ok=%v", mode, ok)
		}

		mode, ok = CaptchaSolveModeForAttempt(1, false)
		if !ok || mode != CaptchaSolveModeManual {
			t.Fatalf("expected second attempt to use manual captcha, got mode=%v ok=%v", mode, ok)
		}

		if _, ok = CaptchaSolveModeForAttempt(2, false); ok {
			t.Fatal("expected only two attempts in default flow")
		}
	})

	t.Run("manual only flow", func(t *testing.T) {
		t.Parallel()

		mode, ok := CaptchaSolveModeForAttempt(0, true)
		if !ok || mode != CaptchaSolveModeManual {
			t.Fatalf("expected manual mode on first attempt, got mode=%v ok=%v", mode, ok)
		}

		if _, ok = CaptchaSolveModeForAttempt(1, true); ok {
			t.Fatal("expected only one manual captcha attempt when manual mode is forced")
		}
	})
}
