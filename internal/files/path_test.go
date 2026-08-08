package files

import "testing"

func TestNormalizeWorkspaceRelativePathAcceptsNestedPath(t *testing.T) {
	normalized, err := NormalizeWorkspaceRelativePath("dir/../nested/file.txt")
	if err != nil {
		t.Fatalf("expected path to be valid, got error: %v", err)
	}
	if normalized != "nested/file.txt" {
		t.Fatalf("expected normalized path nested/file.txt, got %q", normalized)
	}
}

func TestNormalizeWorkspaceRelativePathRejectsTraversal(t *testing.T) {
	_, err := NormalizeWorkspaceRelativePath("../../etc/passwd")
	if err == nil {
		t.Fatal("expected traversal path to be rejected")
	}
}

func TestNormalizeWorkspaceRelativePathRejectsAbsolutePath(t *testing.T) {
	_, err := NormalizeWorkspaceRelativePath("/workspace/notes.txt")
	if err == nil {
		t.Fatal("expected absolute path to be rejected")
	}
}

func TestNormalizeWorkspaceRelativePathRejectsDotPath(t *testing.T) {
	_, err := NormalizeWorkspaceRelativePath(".")
	if err == nil {
		t.Fatal("expected dot path to be rejected")
	}
}

func TestNormalizeWorkspaceRelativePathRejectsNullByte(t *testing.T) {
	_, err := NormalizeWorkspaceRelativePath("hello\x00world")
	if err == nil {
		t.Fatal("expected null byte path to be rejected")
	}
}
