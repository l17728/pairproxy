package auth

import (
	"context"
	"crypto/tls"
	"fmt"
	"testing"

	"github.com/go-ldap/ldap/v3"
	"go.uber.org/zap/zaptest"
)

// ---------------------------------------------------------------------------
// mockLDAPConn — 用于测试的 LDAP 连接 mock
// ---------------------------------------------------------------------------

type mockLDAPConn struct {
	bindCalls    []string // 记录每次 Bind 调用的 DN
	bindErr      map[string]error
	searchResult *ldap.SearchResult
	searchErr    error
}

func (m *mockLDAPConn) Bind(username, password string) error {
	m.bindCalls = append(m.bindCalls, username)
	if err, ok := m.bindErr[username]; ok {
		return err
	}
	return nil
}

func (m *mockLDAPConn) Search(_ *ldap.SearchRequest) (*ldap.SearchResult, error) {
	return m.searchResult, m.searchErr
}

func (m *mockLDAPConn) StartTLS(_ *tls.Config) error { return nil }
func (m *mockLDAPConn) Close() error                 { return nil }

// makeMockDialer 返回一个总是返回给定 mock 连接的 dialer。
func makeMockDialer(conn *mockLDAPConn, dialErr error) ldapDialFn {
	return func(serverAddr string, useTLS bool) (ldapConn, error) {
		if dialErr != nil {
			return nil, dialErr
		}
		return conn, nil
	}
}

// makeEntry 构造一个带指定属性的 LDAP 测试条目。
func makeEntry(dn, uid, mail string) *ldap.Entry {
	attrs := []*ldap.EntryAttribute{
		{Name: "uid", Values: []string{uid}},
		{Name: "mail", Values: []string{mail}},
	}
	return &ldap.Entry{DN: dn, Attributes: attrs}
}

// ---------------------------------------------------------------------------
// TestLDAPProvider_Authenticate_Success
// ---------------------------------------------------------------------------

func TestLDAPProvider_Authenticate_Success(t *testing.T) {
	logger := zaptest.NewLogger(t)
	userDN := "uid=alice,dc=example,dc=com"

	mock := &mockLDAPConn{
		bindErr: map[string]error{},
		searchResult: &ldap.SearchResult{
			Entries: []*ldap.Entry{makeEntry(userDN, "alice-uid", "alice@example.com")},
		},
	}

	p := NewLDAPProvider(logger, LDAPConfig{
		ServerAddr: "ldap.example.com:389",
		BaseDN:     "dc=example,dc=com",
		BindDN:     "cn=service,dc=example,dc=com",
		BindPassword: "svc-pass",
		UserFilter: "(uid=%s)",
	})
	p.dial = makeMockDialer(mock, nil)

	pu, err := p.Authenticate(context.Background(), "alice", "alice-password")
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
	if pu == nil {
		t.Fatal("expected ProviderUser, got nil")
	}
	if pu.ExternalID != "alice-uid" {
		t.Errorf("ExternalID = %q, want alice-uid", pu.ExternalID)
	}
	if pu.Username != "alice" {
		t.Errorf("Username = %q, want alice", pu.Username)
	}
	if pu.Email != "alice@example.com" {
		t.Errorf("Email = %q, want alice@example.com", pu.Email)
	}
	if pu.AuthProvider != "ldap" {
		t.Errorf("AuthProvider = %q, want ldap", pu.AuthProvider)
	}

	// 应发生两次 Bind：服务账户 + 用户 DN
	if len(mock.bindCalls) != 2 {
		t.Errorf("expected 2 Bind calls, got %d: %v", len(mock.bindCalls), mock.bindCalls)
	}
}

// ---------------------------------------------------------------------------
// TestLDAPProvider_Authenticate_WrongPassword
// ---------------------------------------------------------------------------

func TestLDAPProvider_Authenticate_WrongPassword(t *testing.T) {
	logger := zaptest.NewLogger(t)
	userDN := "uid=bob,dc=example,dc=com"

	mock := &mockLDAPConn{
		bindErr: map[string]error{
			userDN: fmt.Errorf("LDAP Result Code 49: Invalid Credentials"),
		},
		searchResult: &ldap.SearchResult{
			Entries: []*ldap.Entry{makeEntry(userDN, "bob", "bob@example.com")},
		},
	}

	p := NewLDAPProvider(logger, LDAPConfig{
		ServerAddr: "ldap.example.com:389",
		BaseDN:     "dc=example,dc=com",
		UserFilter: "(uid=%s)",
	})
	p.dial = makeMockDialer(mock, nil)

	_, err := p.Authenticate(context.Background(), "bob", "wrong-pass")
	if err == nil {
		t.Error("expected error for wrong password, got nil")
	}
}

// ---------------------------------------------------------------------------
// TestLDAPProvider_Authenticate_UserNotFound
// ---------------------------------------------------------------------------

func TestLDAPProvider_Authenticate_UserNotFound(t *testing.T) {
	logger := zaptest.NewLogger(t)

	mock := &mockLDAPConn{
		bindErr:      map[string]error{},
		searchResult: &ldap.SearchResult{Entries: []*ldap.Entry{}}, // empty result
	}

	p := NewLDAPProvider(logger, LDAPConfig{
		ServerAddr: "ldap.example.com:389",
		BaseDN:     "dc=example,dc=com",
		UserFilter: "(uid=%s)",
	})
	p.dial = makeMockDialer(mock, nil)

	_, err := p.Authenticate(context.Background(), "nobody", "pass")
	if err == nil {
		t.Error("expected error for unknown user, got nil")
	}
}

// ---------------------------------------------------------------------------
// TestLDAPProvider_Authenticate_DialError
// ---------------------------------------------------------------------------

func TestLDAPProvider_Authenticate_DialError(t *testing.T) {
	logger := zaptest.NewLogger(t)

	p := NewLDAPProvider(logger, LDAPConfig{
		ServerAddr: "ldap.example.com:389",
		BaseDN:     "dc=example,dc=com",
		UserFilter: "(uid=%s)",
	})
	p.dial = makeMockDialer(nil, fmt.Errorf("connection refused"))

	_, err := p.Authenticate(context.Background(), "alice", "pass")
	if err == nil {
		t.Error("expected dial error, got nil")
	}
}

// ---------------------------------------------------------------------------
// TestLDAPProvider_Authenticate_NoUID_FallbackToUsername
// ---------------------------------------------------------------------------

func TestLDAPProvider_Authenticate_NoUID_FallbackToUsername(t *testing.T) {
	logger := zaptest.NewLogger(t)
	userDN := "cn=carol,dc=example,dc=com"

	// 条目无 uid 属性
	entry := &ldap.Entry{
		DN:         userDN,
		Attributes: []*ldap.EntryAttribute{}, // no uid
	}
	mock := &mockLDAPConn{
		bindErr:      map[string]error{},
		searchResult: &ldap.SearchResult{Entries: []*ldap.Entry{entry}},
	}

	p := NewLDAPProvider(logger, LDAPConfig{
		ServerAddr: "ldap.example.com:389",
		BaseDN:     "dc=example,dc=com",
		UserFilter: "(uid=%s)",
	})
	p.dial = makeMockDialer(mock, nil)

	pu, err := p.Authenticate(context.Background(), "carol", "pass")
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
	// uid 不存在时回退到用户名
	if pu.ExternalID != "carol" {
		t.Errorf("ExternalID = %q, want carol (fallback to username)", pu.ExternalID)
	}
}
