# PairProxy Changelog

## [v2.20.0] - 2026-03-28

### ✨ New Features

#### WebUI Expansion - Phase 1: Group-Target Set Management
- **Target Set Management UI**: Create, update, delete Group-Target Sets with full member management
  - Dual-panel layout for viewing target set list and details
  - Add/remove/update members with inline weight editing
  - Automatic group binding and strategy configuration
  - Full audit logging of all target set operations
  - Member permissions validation (read-only for Worker nodes)

#### WebUI Expansion - Phase 2: Alert Management Enhancement
- **Alert Management Dashboard** with 3 tabs:
  - **Live Tab**: Real-time event streaming with level filtering (error/warn/all)
  - **Active Tab**: Active alerts with batch resolution capability
    - Severity statistics cards (Critical/Error/Warning)
    - Single and batch alert resolution with audit tracking
  - **History Tab**: 90-day alert history with advanced filtering
    - Time range selection (7/30/90 days)
    - Level and source filtering
    - Pagination support (50 items per page)

#### WebUI Expansion - Phase 3: Quick Operations Panel
- **Dashboard Quick Operations Section** on overview page
  - LLM Target Status card: health count, active alerts, target set count
  - System Alerts card: unresolved alert statistics and severity distribution
  - Users/Groups card: active user count, total groups, new users today
  - Async data loading from existing APIs (non-blocking)
  - Quick navigation links to management pages

### 🐛 Bug Fixes

- **Critical**: Fixed template scope issue in target set details panel where `$.SelectedSetID` was incorrectly accessed
- **Critical**: Fixed member delete/update routes to use POST form fields instead of URL path segments (prevents 404 errors)
- **Critical**: Fixed unencoded error messages in redirect URLs that caused malformed HTTP Location headers
- **Important**: Fixed batch alert resolve flash message containing literal space character
- **Important**: Fixed edit target set modal that didn't populate current values
- **Important**: Added ID format validation for target sets (alphanumeric, dash, underscore only)
- **Important**: Fixed redundant time import and custom itoa function

### 🔧 Technical Improvements

- **Code Quality**: All handler implementations follow existing project patterns
  - Middleware chain composition (requireSession + requireWritableNode)
  - Flash message pattern via URL query parameters
  - Audit logging via auditRepo.Create()
  - GORM repository pattern for data access

- **Template Improvements**:
  - Tab-based navigation using URL query parameters (?tab=targetsets)
  - Modal dialog patterns with hidden CSS class toggling
  - Responsive Tailwind CSS grid layouts
  - Named templates for organizing Tab content

- **Data Integrity**:
  - Proper null pointer handling for optional GroupID field
  - N+1 query prevention through batch member loading
  - Cascading delete for target set members
  - Type-safe form field conversions

### 📚 Documentation Updates

- Updated API.md with new dashboard endpoints:
  - `POST /dashboard/llm/targetsets` - Create target set
  - `POST /dashboard/llm/targetsets/{id}/update` - Update target set
  - `POST /dashboard/llm/targetsets/{id}/delete` - Delete target set
  - `POST /dashboard/llm/targetsets/{id}/members` - Add member
  - `POST /dashboard/llm/targetsets/{id}/members/update` - Update member
  - `POST /dashboard/llm/targetsets/{id}/members/delete` - Remove member
  - `POST /dashboard/alerts/resolve` - Resolve single alert
  - `POST /dashboard/alerts/resolve-batch` - Resolve multiple alerts

- Updated manual.md with new UI workflows:
  - Target Set Management workflow
  - Alert Management workflow (live/active/history tabs)
  - Quick Operations panel usage guide

### ✅ Backward Compatibility

- **No Breaking Changes**: All existing APIs and functionality remain unchanged
- **Worker Node Support**: Read-only mode properly enforced for new features
- **Database Schema**: No migrations required; GroupTargetSet and related tables already exist in v2.19+

### 📋 Testing

- All implementations follow existing test patterns:
  - Table-driven test structure
  - In-memory SQLite for integration tests
  - httptest for HTTP handler testing
  - Testify assertions and require patterns
  - Full audit logging verification

### 🎯 Known Limitations

- Alert resolution handlers currently log the action but don't modify in-memory event state (future enhancement)
- LLM target health status requires separate API integration (placeholder in quick ops panel)
- Quick operations panel uses cached data (5-minute TTL for user stats)

### 🚀 Deployment Notes

- No database migrations required
- No configuration changes needed
- Fully backward compatible with v2.19.x deployments
- All new features are optional (repo dependencies check for nil before use)
- Worker nodes automatically enforced read-only mode for new features

---

## [v2.19.0] - 2026-03-15

(See previous releases for v2.19 and earlier changes)
