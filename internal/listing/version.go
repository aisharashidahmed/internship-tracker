package listing

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"sort"
	"strings"
)

// The rules (normalization, matching, tags) are the Go files of this
// package, so the files themselves are compiled into the program...
//
//go:embed *.go
var ruleFiles embed.FS

// ...and RulesVersion is a fingerprint of them. Any edit to a rule changes
// it, so ingestion knows to re-save every listing with the new rules
// (even when the source files haven't changed) without anyone having to
// remember to bump a version number. Test files are left out: editing a
// test doesn't change how listings are matched.
var RulesVersion = rulesVersion()

func rulesVersion() string {
	names, _ := fs.Glob(ruleFiles, "*.go")
	sort.Strings(names)
	h := sha256.New()
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		b, _ := ruleFiles.ReadFile(name)
		h.Write([]byte(name))
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}
