package startwork_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/coredev-uk/ade/internal/forge/gitlab/fake"
	"github.com/coredev-uk/ade/internal/startwork"
)

// A team keeps its MR templates in one repo: every repo can use them, and a
// repo's own template of the same name wins.
func TestTemplateProjectOffersItsTemplatesToEveryRepo(t *testing.T) {
	h := newHarness(t, fake.Free)
	ctx := context.Background()
	templates := h.srv.AddProject("templates")
	h.srv.AddFile(templates, startwork.TemplateDir+"/Default.md", "team default\n")
	h.srv.AddFile(templates, startwork.TemplateDir+"/Feature.md", "team feature\n")
	h.m.Conventions.MR.TemplateProject = templates

	// web has no templates of its own: the team's Default applies.
	got, err := startwork.PickTemplate(ctx, h.forge, h.m, webProj, "main", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Default" || got.Body != "team default\n" || len(got.Warnings) != 0 {
		t.Errorf("web = %+v, want the template project's Default", got)
	}

	// api has its own Default, which wins; the team's Feature is offered too.
	got, err = startwork.PickTemplate(ctx, h.forge, h.m, apiProj, "main", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Body != defaultTemplate || !slices.Equal(got.Names, []string{"Default", "Feature"}) {
		t.Errorf("api = %+v, want its own Default and the names [Default Feature]", got)
	}
	h.m.Conventions.MR.Template = "Feature"
	if got, err = startwork.PickTemplate(ctx, h.forge, h.m, apiProj, "main", ""); err != nil || got.Body != "team feature\n" {
		t.Errorf("api with template = Feature: %+v, %v", got, err)
	}

	// A template project that does not exist is a warning, not a failure.
	h.m.Conventions.MR.Template = ""
	h.m.Conventions.MR.TemplateProject = "demo/nope"
	got, err = startwork.PickTemplate(ctx, h.forge, h.m, apiProj, "main", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Body != defaultTemplate || len(got.Warnings) != 1 || !strings.Contains(got.Warnings[0], "template project demo/nope could not be read") {
		t.Errorf("missing template project = %+v", got)
	}
}
