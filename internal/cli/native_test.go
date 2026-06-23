package cli

import "testing"

// TestEditorResolutionVisualFirst verifies that resolveEditor returns $VISUAL
// when it is set, regardless of $EDITOR.
func TestEditorResolutionVisualFirst(t *testing.T) {
	t.Setenv("VISUAL", "my-visual-editor")
	t.Setenv("EDITOR", "my-plain-editor")
	if got := resolveEditor(); got != "my-visual-editor" {
		t.Errorf("resolveEditor: expected VISUAL %q, got %q", "my-visual-editor", got)
	}
}

// TestEditorResolutionEditorFallback verifies that resolveEditor falls back to
// $EDITOR when $VISUAL is unset.
func TestEditorResolutionEditorFallback(t *testing.T) {
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "my-plain-editor")
	if got := resolveEditor(); got != "my-plain-editor" {
		t.Errorf("resolveEditor: expected EDITOR %q, got %q", "my-plain-editor", got)
	}
}

// TestEditorResolutionViFallback verifies that resolveEditor falls back to vi
// when neither $VISUAL nor $EDITOR is set.
func TestEditorResolutionViFallback(t *testing.T) {
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")
	if got := resolveEditor(); got != "vi" {
		t.Errorf("resolveEditor: expected vi fallback, got %q", got)
	}
}
