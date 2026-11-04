package policy

import (
	"testing"

	"github.com/SSStq1314/hengdesk/server/internal/ent"
	"github.com/SSStq1314/hengdesk/server/internal/ent/tool"
)

func TestDecide(t *testing.T) {
	sid := 7
	srv := &ent.McpServer{ID: 7, OrgID: 1, Enabled: true}
	base := func() *ent.Tool {
		return &ent.Tool{OrgID: 1, ServerID: &sid, Enabled: true, ExecMode: tool.ExecModeAuto, RiskLevel: tool.RiskLevelReadOnly}
	}
	cases := []struct {
		name   string
		mod    func(*ent.Tool)
		srv    *ent.McpServer
		usable bool
		mode   string
		reason string
	}{
		{"ok", func(*ent.Tool) {}, srv, true, ModeAuto, ""},
		{"disabled", func(t *ent.Tool) { t.Enabled = false }, srv, false, "", ReasonDisabled},
		{"deny", func(t *ent.Tool) { t.ExecMode = tool.ExecModeDeny }, srv, false, "", ReasonDenied},
		{"review", func(t *ent.Tool) { t.NeedsReview = true }, srv, false, "", ReasonNeedsReview},
		{"removed", func(t *ent.Tool) { t.Removed = true }, srv, false, "", ReasonRemoved},
		{"other org", func(t *ent.Tool) { t.OrgID = 2 }, srv, false, "", ReasonOtherOrg},
		{"server off", func(*ent.Tool) {}, &ent.McpServer{ID: 7, OrgID: 1}, false, "", ReasonServerOff},
		{"server missing", func(*ent.Tool) {}, nil, false, "", ReasonServerAbsent},
		{"approval", func(t *ent.Tool) { t.ExecMode = tool.ExecModeRequireApproval }, srv, true, ModeApproval, ""},
		{"high auto forced", func(t *ent.Tool) { t.RiskLevel = tool.RiskLevelHighRisk }, srv, true, ModeApproval, ""},
	}
	for _, c := range cases {
		tl := base()
		c.mod(tl)
		d := Decide(1, tl, c.srv)
		if d.Usable != c.usable || (c.usable && d.Mode != c.mode) || d.Reason != c.reason {
			t.Errorf("%s: %+v", c.name, d)
		}
	}
}

func TestValidateSettings(t *testing.T) {
	if ValidateSettings(RiskHigh, ModeAuto) == "" {
		t.Fatal("high+auto must be rejected")
	}
	for _, ok := range [][2]string{{RiskHigh, ModeApproval}, {RiskWrite, ModeAuto}, {RiskReadOnly, ModeDeny}} {
		if m := ValidateSettings(ok[0], ok[1]); m != "" {
			t.Fatal(m)
		}
	}
	if ValidateSettings("x", ModeAuto) == "" || ValidateSettings(RiskWrite, "x") == "" {
		t.Fatal("unknown values")
	}
}
