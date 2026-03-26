// Package prompts 管理预定义的 prompt 库
package prompts

import (
	"fmt"
	"math/rand"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Loader 从 YAML 文件加载 prompts
type Loader struct {
	categories map[string][]string
	rng        *rand.Rand
}

// Config YAML 配置结构
type Config struct {
	Categories map[string]Category `yaml:"categories"`
}

// Category 单个类别的 prompts
type Category struct {
	Prompts []string `yaml:"prompts"`
}

// NewLoader 创建新的 prompt loader
func NewLoader(path string) (*Loader, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read prompts file: %w", err)
	}

	var config Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("parse prompts file: %w", err)
	}

	// 构建扁平化的 prompt 列表
	categories := make(map[string][]string)
	for name, cat := range config.Categories {
		if len(cat.Prompts) > 0 {
			categories[name] = cat.Prompts
		}
	}

	return &Loader{
		categories: categories,
		rng:        rand.New(rand.NewSource(time.Now().UnixNano())),
	}, nil
}

// NewLoaderWithData 从内存数据创建 loader（用于测试）
func NewLoaderWithData(categories map[string][]string) *Loader {
	return &Loader{
		categories: categories,
		rng:        rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// GetRandom 随机获取一个 prompt
func (l *Loader) GetRandom() string {
	// 随机选择一个 category
	var allPrompts []string
	for _, prompts := range l.categories {
		allPrompts = append(allPrompts, prompts...)
	}

	if len(allPrompts) == 0 {
		return "Hello, explain this code to me."
	}

	return allPrompts[l.rng.Intn(len(allPrompts))]
}

// GetRandomFromCategory 从指定 category 随机获取
func (l *Loader) GetRandomFromCategory(category string) string {
	prompts, ok := l.categories[category]
	if !ok || len(prompts) == 0 {
		return l.GetRandom()
	}
	return prompts[l.rng.Intn(len(prompts))]
}

// GetCategories 返回所有可用的 category 名称
func (l *Loader) GetCategories() []string {
	var names []string
	for name := range l.categories {
		names = append(names, name)
	}
	return names
}

// GetCategoryPrompts 返回指定 category 的所有 prompts
func (l *Loader) GetCategoryPrompts(category string) []string {
	if prompts, ok := l.categories[category]; ok {
		return prompts
	}
	return nil
}

// DefaultPrompts 返回默认的 prompts（当没有配置文件时使用）
func DefaultPrompts() map[string][]string {
	return map[string][]string{
		"code_understanding": {
			"解释这段代码的作用：func main() { fmt.Println(\"Hello, World!\") }",
			"这段 Python 代码是什么意思：def foo(): return [x for x in range(10)]",
			"分析这个正则表达式：^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\\.[a-zA-Z]{2,}$",
			"解释 SQL 查询：SELECT * FROM users WHERE age > 18 ORDER BY name",
			"这段 JavaScript 代码的时间复杂度是多少？function loop(n) { for(let i=0; i<n; i++) console.log(i); }",
		},
		"code_refactoring": {
			"重构这段代码，使其更易读：if (x == 1) { return true; } else { return false; }",
			"将这段代码改为使用泛型：func max(a, b int) int { if a > b { return a } return b }",
			"优化这段 SQL 查询，添加适当的索引建议",
			"将这个回调函数改为 async/await 风格",
			"重构这个类，使用依赖注入模式",
		},
		"debugging": {
			"这段代码报错 'nil pointer dereference'，如何修复？",
			"为什么会出现 'concurrent map writes' 错误？",
			"这个 SQL 查询返回了重复的数据，如何解决？",
			"程序死锁了，如何排查？",
			"内存泄漏问题，如何定位？",
		},
		"code_generation": {
			"写一个函数，判断字符串是否是回文",
			"生成一个 REST API 的 CRUD 代码模板",
			"写一个快速排序算法",
			"生成一个 Docker Compose 配置文件，包含 PostgreSQL 和 Redis",
			"写一个单元测试，测试这个函数的各种边界条件",
		},
		"explanation": {
			"什么是死锁？如何避免？",
			"解释 CAP 定理",
			"什么是 RESTful API？",
			"解释 OAuth 2.0 的工作流程",
			"什么是微服务架构的优缺点？",
		},
	}
}
