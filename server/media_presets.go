package main

import "path/filepath"

// MediaPreset defines the processing policy for each /commit category.
type MediaPreset struct {
	MaxWidth      int                 // 0 = keep original width; 728 = standard article/comment width
	AllowAnimated bool                // whether animated images are accepted and transcoded
	OutputDir     func() string       // physical output dir for the main image; resolved per request
	URLGenerator  func(string) string // url generator for the resulting files
	GenerateThumb bool                // whether to generate a companion thumbnail
	DeviceDetect  bool                // whether to auto-detect landscape/portrait orientation
	AdminOnly     bool                // whether admin permission is required
}

// CommitPresets is the declarative config dictionary for every upload scenario (zero hardcoded literals).
//
// OutputDir is a function rather than a plain string on purpose. The Dir* paths it
// returns are assigned by initDerivedPaths(), which runs during package-variable
// initialisation but *after* this map is built — capturing them here would freeze
// every OutputDir at "" and uploads would silently land in the working directory
// with a URL like "//<id>.avif" (a protocol-relative host, i.e. a broken image).
var CommitPresets = map[string]MediaPreset{
	// 1. Article illustrations: 728px cap, animated allowed, admin only
	"post": {
		MaxWidth:      728,
		AllowAnimated: true,
		OutputDir:     func() string { return DirImagePost },
		URLGenerator:  URLImagePost,
		AdminOnly:     true,
	},
	// 2. Comment images: 728px cap, animated allowed, public
	"comment": {
		MaxWidth:      728,
		AllowAnimated: true,
		OutputDir:     func() string { return DirImageComment },
		URLGenerator:  URLImageComment,
		AdminOnly:     false,
	},
	// 3. Photo album: full resolution, no scaling + auto-generate 230px blurred thumb into DirThumbPhotoBlur
	"photo": {
		MaxWidth:      0,
		AllowAnimated: false,
		OutputDir:     func() string { return DirPhoto },
		URLGenerator:  URLPhoto,
		GenerateThumb: true,
		AdminOnly:     true,
	},
	// 4. Site background: no scaling + auto landscape/portrait classification
	//
	// Declared for completeness, but background uploads do NOT go through
	// ProcessMediaPipeline: they must append a BgItem (device / tone / offset) to
	// config/background.json, which the pipeline — it only returns a URL — cannot do.
	// They are handled by handleAdminBgUpload instead. See handleCommitGateway.
	"background": {
		MaxWidth:      0,
		AllowAnimated: false,
		OutputDir:     func() string { return DirBackground },
		URLGenerator:  func(f string) string { return filepath.ToSlash(filepath.Join(PrefixAssets, "image/background", f)) },
		DeviceDetect:  true,
		AdminOnly:     true,
	},
}
