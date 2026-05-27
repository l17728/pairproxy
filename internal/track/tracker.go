// Package track 提供基于文件系统的用户对话跟踪功能。
//
// 跟踪功能通过 shell 命令启用/禁用，无需 UI 或 API。
// 跟踪数据存储在 <db_dir>/track/ 目录下：
//
//	track/
//	  users/          — 标记文件目录：文件存在 = 该用户已启用跟踪
//	    alice          — 空文件，表示 alice 已被跟踪
//	  conversations/  — 对话记录目录
//	    alice/
//	      2026-03-07T12:34:56Z-req-abc123.json
package track

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Tracker 管理每用户跟踪状态（基于文件系统标记文件）。
// 所有方法均为线程安全：只依赖文件系统原子操作（create/remove/readdir）。
type Tracker struct {
	dir string // 跟踪根目录（例如 <db_dir>/track）
}

// New 创建 Tracker 并确保目录结构存在。
// dir 为跟踪根目录，如果目录不存在会自动创建。
func New(dir string) (*Tracker, error) {
	for _, sub := range []string{usersDir(dir), convsDir(dir)} {
		if err := os.MkdirAll(sub, 0o755); err != nil {
			return nil, err
		}
	}
	return &Tracker{dir: dir}, nil
}

// IsTracked 报告指定用户是否已启用跟踪。
// 若标记文件存在则返回 true，否则（含 I/O 错误）返回 false。
func (t *Tracker) IsTracked(username string) bool {
	_, err := os.Stat(markerPath(t.dir, username))
	return err == nil
}

// Enable 为指定用户启用跟踪（创建标记文件和对话目录）。
// 已启用时幂等，不报错。
func (t *Tracker) Enable(username string) error {
	if err := validateUsername(username); err != nil {
		return err
	}
	// 确保对话目录存在（0o777 允许不同用户写入，适配 CLI 以 root 创建、service 以其他用户写入的场景）
	if err := os.MkdirAll(userConvDir(t.dir, username), 0o777); err != nil {
		return err
	}
	// 创建标记文件（幂等：已存在不报错）
	f, err := os.OpenFile(markerPath(t.dir, username), os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	return f.Close()
}

// Disable 为指定用户停用跟踪（删除标记文件）。
// 未启用时幂等，不报错。对话记录文件不会被删除。
func (t *Tracker) Disable(username string) error {
	if err := validateUsername(username); err != nil {
		return err
	}
	err := os.Remove(markerPath(t.dir, username))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// ListTracked 返回当前已启用跟踪的所有用户名列表，按文件系统顺序排列。
func (t *Tracker) ListTracked() ([]string, error) {
	entries, err := os.ReadDir(usersDir(t.dir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

// Dir 返回跟踪根目录路径。
func (t *Tracker) Dir() string { return t.dir }

// UserConvDir 返回指定用户的对话记录目录路径。
func (t *Tracker) UserConvDir(username string) string {
	return userConvDir(t.dir, username)
}

// ---------------------------------------------------------------------------
// 内部路径工具
// ---------------------------------------------------------------------------

func usersDir(root string) string       { return filepath.Join(root, "users") }
func convsDir(root string) string       { return filepath.Join(root, "conversations") }
func markerPath(root, user string) string { return filepath.Join(usersDir(root), user) }
func userConvDir(root, user string) string {
	return filepath.Join(convsDir(root), user)
}

// validateUsername 拒绝空字符串、".." 和包含路径分隔符的非法用户名，防止路径遍历。
func validateUsername(username string) error {
	if username == "" {
		return errors.New("track: username must not be empty")
	}
	if strings.ContainsAny(username, "/\\") || strings.Contains(username, "..") {
		return errors.New("track: username contains invalid characters (/ \\ or ..)")
	}
	return nil
}
