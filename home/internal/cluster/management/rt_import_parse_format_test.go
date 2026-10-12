package management

import "testing"

// Fake token with the same shape as real Google refresh tokens ("1//06" prefix, contains '-').
const fakeRTShape = "1//06FakeTokenCgYIARAAGAYSNwF-L9IrFakeFakeFakeFake-abc_DEF0123456789xyzE68s"

func TestParseRefreshTokenInput_EmailSeparatorVariants(t *testing.T) {
	cases := map[string]string{
		"plain":           "user.x-y@gmail.com----" + fakeRTShape,
		"crlf":            "user.x-y@gmail.com----" + fakeRTShape + "\r\n",
		"bom_zero_width":  "\ufeffuser.x-y@gmail.com\u200b----" + fakeRTShape + "\u200b",
		"three_dashes":    "user.x-y@gmail.com---" + fakeRTShape,
		"five_dashes":     "user.x-y@gmail.com-----" + fakeRTShape,
		"fullwidth":       "user.x-y@gmail.com－－－－" + fakeRTShape,
		"em_dash":         "user.x-y@gmail.com——" + "——" + fakeRTShape,
		"spaces":          "user.x-y@gmail.com ---- " + fakeRTShape,
		"nbsp":            "\u00a0user.x-y@gmail.com----" + fakeRTShape + "\u00a0",
		"extra_fields":    "user.x-y@gmail.com----" + fakeRTShape + "----password----x",
		"cr_only_newline": "user.x-y@gmail.com----" + fakeRTShape + "\r",
	}
	for name, in := range cases {
		got := parseRefreshTokenInput(in)
		if len(got) != 1 || got[0].ParseError != "" || got[0].RefreshToken != fakeRTShape || got[0].Email != "user.x-y@gmail.com" {
			t.Errorf("%s: got %+v", name, got)
		}
	}
	if got := parseRefreshTokenInput(fakeRTShape); len(got) != 1 || got[0].RefreshToken != fakeRTShape || got[0].Email != "" {
		t.Errorf("bare token: %+v", got)
	}
	if got := parseRefreshTokenInput("notanemail----" + fakeRTShape); len(got) != 1 || got[0].ParseError == "" {
		t.Errorf("missing @ should fail: %+v", got)
	}
}
