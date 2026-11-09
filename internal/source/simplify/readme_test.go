package simplify

import (
	"slices"
	"testing"

	"internship-tracker/internal/listing"
)

func TestTopCompanies(t *testing.T) {
	readme := `
- 🔥 FAANG+ company
<table>
<tr>
<td>🔥 <strong><a href="https://simplify.jobs/c/TikTok?utm_source=GHList">TikTok</a></strong></td>
<td>Software Engineer Intern 🎓</td>
</tr>
<tr>
<td>↳</td>
<td>ML Engineer Intern</td>
</tr>
<tr>
<td><strong><a href="https://simplify.jobs/c/Shield-AI">Shield AI</a></strong></td>
<td>Engineer 2 New Grad</td>
</tr>
<tr>
<td>🔥 <strong><a href="https://simplify.jobs/c/AT-T">AT&amp;T</a></strong></td>
<td>Intern</td>
</tr>
<tr>
<td>🔥 <strong><a href="https://simplify.jobs/c/TikTok">TikTok</a></strong></td>
<td>Data Intern</td>
</tr>
</table>`
	got, err := TopCompanies([]byte(readme))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"TikTok", "AT&T"}) {
		t.Errorf("top companies = %q, want [TikTok AT&T] (the legend line and unmarked rows don't count)", got)
	}
	// The flag was read before cleanup, and the cleaned name still
	// matches the company as stored ("🔥 TikTok" and "TikTok" are one company).
	if listing.NormalizeCompany("🔥 TikTok") != listing.NormalizeCompany(got[0]) {
		t.Error("the marked name should normalize to the same company")
	}
}

func TestTopCompanies_FailsLoudlyOnFormatChange(t *testing.T) {
	if _, err := TopCompanies([]byte("# no tables any more\n| Company | Role |\n")); err == nil {
		t.Error("want an error when the README has no table rows")
	}
}
