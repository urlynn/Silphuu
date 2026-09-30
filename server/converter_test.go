package main

import "testing"

// TestNeedsPNGIntermediate pins the rule that decides which uploads are decoded before an
// encoder sees them.
//
// The table is the measured behaviour of the deployment's toolchain, not a preference: cjxl
// -d 0 -m 1 -e 10 exits 1 with "Getting pixel data failed." on WebP and BMP, and avifenc exits 1
// with "Couldn't read unknown file" on WebP, BMP and GIF. A format only one of the two reads is
// therefore still a format that has to be normalized — a GIF upload passed the JXL step and then
// failed the AVIF one.
func TestNeedsPNGIntermediate(t *testing.T) {
	cases := []struct {
		ext     string
		formats []string
		want    bool
	}{
		// Both encoders read these, so they go straight through.
		{".png", []string{"jxl", "avif"}, false},
		{".jpg", []string{"jxl", "avif"}, false},
		{".jpeg", []string{"jxl", "avif"}, false},

		// cjxl cannot decode these at all.
		{".webp", []string{"jxl", "avif"}, true},
		{".bmp", []string{"jxl", "avif"}, true},
		// avifenc cannot decode this one, though cjxl can.
		{".gif", []string{"jxl", "avif"}, true},

		// Formats that reach the encoders on their own paths today.
		{".heic", []string{"jxl", "avif"}, true},
		{".heif", []string{"jxl", "avif"}, true},
		{".jxl", []string{"jxl", "avif"}, true},
		{".avif", []string{"jxl", "avif"}, true},
		{".tiff", []string{"jxl", "avif"}, true},

		// An extension nobody has heard of is normalized rather than handed over.
		{".psd", []string{"jxl", "avif"}, true},
		{"", []string{"jxl", "avif"}, true},

		// No encoder target means no encoder, so there is nothing to decode for.
		{".webp", nil, false},
		{".webp", []string{}, false},
		{".webp", []string{"png"}, false},
	}

	for _, c := range cases {
		if got := needsPNGIntermediate(c.ext, c.formats); got != c.want {
			t.Errorf("needsPNGIntermediate(%q, %v) = %v, want %v", c.ext, c.formats, got, c.want)
		}
	}
}

// TestEncoderInputsAreOnlyWhatBothEncodersRead: the pass-through set is the intersection of what
// cjxl and avifenc decode, and it is small on purpose. Widening it without re-measuring both
// encoders hands one of them a file it answers with an error.
func TestEncoderInputsAreOnlyWhatBothEncodersRead(t *testing.T) {
	want := map[string]bool{".png": true, ".jpg": true, ".jpeg": true}
	if len(encoderInputs) != len(want) {
		t.Fatalf("encoderInputs = %v, want exactly %v", encoderInputs, want)
	}
	for ext := range want {
		if !encoderInputs[ext] {
			t.Errorf("encoderInputs is missing %q", ext)
		}
	}
}
