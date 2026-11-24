package eventingapp

// v0.2.4-D T8 (SC-D3/R-D5): the run:{id} transcript channel. The channel name
// has ONE producer (RunChannel) and ONE recognizer (IsRunChannel), both derived
// from the same whitelist pattern the publish/snapshot path validates against,
// so a publisher and a subscriber can never disagree about the channel string.

import "testing"

func TestRunChannelIsAValidChannel(t *testing.T) {
	cases := map[string]bool{
		"run:1":      true,
		"run:999999": true,
		"run:":       false,
		"run:0":      false,
		"run:abc":    false,
		"run:-1":     false,
		"run:1:2":    false,
		"runx:1":     false,
		"runs:1":     false,
	}
	for channel, want := range cases {
		if got := ValidChannel(channel); got != want {
			t.Fatalf("ValidChannel(%q) = %v, want %v", channel, got, want)
		}
	}
}

func TestRunChannelProducerAndRecognizerAgree(t *testing.T) {
	for _, runID := range []int64{1, 42, 9007199254740993} {
		channel := RunChannel(runID)
		if !ValidChannel(channel) {
			t.Fatalf("RunChannel(%d) = %q is not a valid channel", runID, channel)
		}
		if !IsRunChannel(channel) {
			t.Fatalf("IsRunChannel(%q) = false for the produced run channel", channel)
		}
	}
	// The gate must not fire on the other channels: a Host conversation or the
	// system channel stays readable by any logged-in user (SC-D3 regression).
	for _, channel := range []string{"system", "version:7", "user:42", "", "run:abc", "run:0"} {
		if IsRunChannel(channel) {
			t.Fatalf("IsRunChannel(%q) = true, want false", channel)
		}
	}
}
