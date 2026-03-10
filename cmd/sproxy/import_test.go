package main

import (
	"os"
	"path/filepath"
	"testing"
)

// writeImportFile 在临时目录创建导入文件，返回文件路径。
func writeImportFile(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "import-*.txt")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	return f.Name()
}

// TestParseImportFile_Basic 验证基本分组+用户解析。
func TestParseImportFile_Basic(t *testing.T) {
	path := writeImportFile(t, `
[engineering]
alice  Password123
bob    Password456

[marketing]
charlie  Marketing789
`)
	sections, err := parseImportFile(path)
	if err != nil {
		t.Fatalf("parseImportFile: %v", err)
	}

	// 文件开头有一个隐式空 section（无分组），所以共 3 个 section
	if len(sections) != 3 {
		t.Fatalf("sections count = %d, want 3", len(sections))
	}

	eng := sections[1]
	if eng.GroupName != "engineering" {
		t.Errorf("section[1].GroupName = %q, want %q", eng.GroupName, "engineering")
	}
	if len(eng.Users) != 2 {
		t.Fatalf("engineering users = %d, want 2", len(eng.Users))
	}
	if eng.Users[0].Username != "alice" || eng.Users[0].Password != "Password123" {
		t.Errorf("users[0] = %+v", eng.Users[0])
	}

	mkt := sections[2]
	if mkt.GroupName != "marketing" {
		t.Errorf("section[2].GroupName = %q, want %q", mkt.GroupName, "marketing")
	}
	if len(mkt.Users) != 1 {
		t.Fatalf("marketing users = %d, want 1", len(mkt.Users))
	}
}

// TestParseImportFile_LLMBinding 验证组级和用户级 llm=URL 解析。
func TestParseImportFile_LLMBinding(t *testing.T) {
	path := writeImportFile(t, `
[engineering llm=https://api.anthropic.com]
alice  Password123
bob    Password456 llm=https://api.openai.com
`)
	sections, err := parseImportFile(path)
	if err != nil {
		t.Fatalf("parseImportFile: %v", err)
	}

	eng := sections[1]
	if eng.LLMTarget != "https://api.anthropic.com" {
		t.Errorf("group LLMTarget = %q, want https://api.anthropic.com", eng.LLMTarget)
	}
	if eng.Users[0].LLMOverride != "" {
		t.Errorf("alice LLMOverride = %q, want empty", eng.Users[0].LLMOverride)
	}
	if eng.Users[1].LLMOverride != "https://api.openai.com" {
		t.Errorf("bob LLMOverride = %q, want https://api.openai.com", eng.Users[1].LLMOverride)
	}
}

// TestParseImportFile_Comments 验证注释和空行被跳过。
func TestParseImportFile_Comments(t *testing.T) {
	path := writeImportFile(t, `
# 这是注释
; 这也是注释

[engineering]
# 另一条注释
alice Password123

`)
	sections, err := parseImportFile(path)
	if err != nil {
		t.Fatalf("parseImportFile: %v", err)
	}

	eng := sections[1]
	if len(eng.Users) != 1 {
		t.Errorf("users = %d, want 1 (comments should be skipped)", len(eng.Users))
	}
}

// TestParseImportFile_NoGroup 验证文件头部（无分组头）的用户视为无分组。
func TestParseImportFile_NoGroup(t *testing.T) {
	path := writeImportFile(t, `
dave NoGroup_Pass

[engineering]
alice Password123
`)
	sections, err := parseImportFile(path)
	if err != nil {
		t.Fatalf("parseImportFile: %v", err)
	}

	// sections[0] = 隐式无分组区块，含 dave
	noGroup := sections[0]
	if noGroup.GroupName != "" {
		t.Errorf("implicit no-group section GroupName = %q, want empty", noGroup.GroupName)
	}
	if len(noGroup.Users) != 1 || noGroup.Users[0].Username != "dave" {
		t.Errorf("no-group users = %+v, want [{dave ...}]", noGroup.Users)
	}
}

// TestParseImportFile_DashGroup 验证 [-] 语法被解析为无分组。
func TestParseImportFile_DashGroup(t *testing.T) {
	path := writeImportFile(t, `
[engineering]
alice Password123

[-]
dave NoGroup_Pass
`)
	sections, err := parseImportFile(path)
	if err != nil {
		t.Fatalf("parseImportFile: %v", err)
	}

	// sections: [0]=头部空区块, [1]=engineering, [2]=[-]
	dash := sections[2]
	if dash.GroupName != "" {
		t.Errorf("[-] section GroupName = %q, want empty", dash.GroupName)
	}
	if len(dash.Users) != 1 || dash.Users[0].Username != "dave" {
		t.Errorf("[-] users = %+v, want [{dave ...}]", dash.Users)
	}
}

// TestParseImportFile_EmptyGroup 验证空分组（无用户）也能正确解析。
func TestParseImportFile_EmptyGroup(t *testing.T) {
	path := writeImportFile(t, `
[empty-group]

[engineering]
alice Password123
`)
	sections, err := parseImportFile(path)
	if err != nil {
		t.Fatalf("parseImportFile: %v", err)
	}

	// sections: [0]=头部空, [1]=empty-group(0用户), [2]=engineering(1用户)
	if len(sections) != 3 {
		t.Fatalf("sections = %d, want 3", len(sections))
	}
	emptyGrp := sections[1]
	if emptyGrp.GroupName != "empty-group" {
		t.Errorf("GroupName = %q, want empty-group", emptyGrp.GroupName)
	}
	if len(emptyGrp.Users) != 0 {
		t.Errorf("empty-group users = %d, want 0", len(emptyGrp.Users))
	}
}

// TestParseImportFile_MissingPassword 验证缺少密码时返回错误。
func TestParseImportFile_MissingPassword(t *testing.T) {
	path := writeImportFile(t, `
[engineering]
alice
`)
	_, err := parseImportFile(path)
	if err == nil {
		t.Fatal("expected error for missing password, got nil")
	}
}

// TestParseImportFile_FileNotFound 验证文件不存在时返回错误。
func TestParseImportFile_FileNotFound(t *testing.T) {
	_, err := parseImportFile(filepath.Join(t.TempDir(), "nonexistent.txt"))
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

// TestParseImportFile_WindowsCRLF 验证 \r\n 换行符能被正确解析。
func TestParseImportFile_WindowsCRLF(t *testing.T) {
	// Write raw bytes with CRLF line endings.
	dir := t.TempDir()
	fpath := filepath.Join(dir, "import-crlf.txt")
	content := "[engineering]\r\nalice  Password123\r\nbob    Password456\r\n"
	if err := os.WriteFile(fpath, []byte(content), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	sections, err := parseImportFile(fpath)
	if err != nil {
		t.Fatalf("parseImportFile with CRLF: %v", err)
	}

	// sections[0] = implicit empty head section, sections[1] = engineering
	var eng importSection
	for _, s := range sections {
		if s.GroupName == "engineering" {
			eng = s
			break
		}
	}
	if len(eng.Users) != 2 {
		t.Fatalf("engineering users = %d, want 2", len(eng.Users))
	}
	if eng.Users[0].Username != "alice" {
		t.Errorf("users[0].Username = %q, want alice", eng.Users[0].Username)
	}
	if eng.Users[1].Username != "bob" {
		t.Errorf("users[1].Username = %q, want bob", eng.Users[1].Username)
	}
}

// TestParseImportFile_DuplicateGroups 验证两个同名 [engineering] section 各自独立解析（共 3 个 section）。
func TestParseImportFile_DuplicateGroups(t *testing.T) {
	path := writeImportFile(t, `
[engineering]
eng1  Password111

[engineering]
eng2  Password222
`)
	sections, err := parseImportFile(path)
	if err != nil {
		t.Fatalf("parseImportFile: %v", err)
	}

	// sections: [0]=head(empty), [1]=engineering(eng1), [2]=engineering(eng2)
	if len(sections) != 3 {
		t.Fatalf("sections = %d, want 3 (head + eng1 section + eng2 section)", len(sections))
	}
	if sections[1].Users[0].Username != "eng1" {
		t.Errorf("sections[1] user = %q, want eng1", sections[1].Users[0].Username)
	}
	if sections[2].Users[0].Username != "eng2" {
		t.Errorf("sections[2] user = %q, want eng2", sections[2].Users[0].Username)
	}
}

// TestParseImportFile_DuplicateUsers 验证同名用户在同一分组中均被追加（解析器不去重）。
func TestParseImportFile_DuplicateUsers(t *testing.T) {
	path := writeImportFile(t, `
[engineering]
alice  Password123
alice  Password456
`)
	sections, err := parseImportFile(path)
	if err != nil {
		t.Fatalf("parseImportFile: %v", err)
	}

	eng := sections[1]
	if len(eng.Users) != 2 {
		t.Fatalf("engineering users = %d, want 2 (duplicate not deduped by parser)", len(eng.Users))
	}
	if eng.Users[0].Username != "alice" || eng.Users[1].Username != "alice" {
		t.Errorf("expected both users to be alice, got %q and %q", eng.Users[0].Username, eng.Users[1].Username)
	}
}

// TestParseImportFile_MalformedLLMEmpty 验证 "llm=" 值为空时的行为。
func TestParseImportFile_MalformedLLMEmpty(t *testing.T) {
	path := writeImportFile(t, `
[engineering]
alice pass llm=
`)
	sections, err := parseImportFile(path)
	if err != nil {
		// Error is acceptable if the parser rejects empty LLM URL.
		return
	}
	// If parser succeeds, alice should have LLMOverride == "" (empty string after "llm=").
	eng := sections[1]
	if len(eng.Users) != 1 {
		t.Fatalf("engineering users = %d, want 1", len(eng.Users))
	}
	if eng.Users[0].LLMOverride != "" {
		t.Errorf("LLMOverride = %q, want empty string for 'llm='", eng.Users[0].LLMOverride)
	}
}

// TestParseImportFile_ExtraFieldsIgnored 验证额外字段被忽略，llm= URL 仍能正确解析。
func TestParseImportFile_ExtraFieldsIgnored(t *testing.T) {
	path := writeImportFile(t, `
[engineering]
alice pass extra1 extra2 llm=https://api.anthropic.com
`)
	sections, err := parseImportFile(path)
	if err != nil {
		t.Fatalf("parseImportFile: %v", err)
	}

	eng := sections[1]
	if len(eng.Users) != 1 {
		t.Fatalf("engineering users = %d, want 1", len(eng.Users))
	}
	u := eng.Users[0]
	if u.Username != "alice" {
		t.Errorf("Username = %q, want alice", u.Username)
	}
	if u.Password != "pass" {
		t.Errorf("Password = %q, want pass", u.Password)
	}
	if u.LLMOverride != "https://api.anthropic.com" {
		t.Errorf("LLMOverride = %q, want https://api.anthropic.com", u.LLMOverride)
	}
}
