package tokenizer

import (
	tiktoken "github.com/pkoukk/tiktoken-go"
)

// loadCL100K 加载 cl100k_base 编码器。
// 首次调用时从本地缓存（~/.tiktoken 或 TIKTOKEN_CACHE_DIR）读取词表文件；
// 若缓存不存在则自动下载（约 1.7MB）。
// 后续调用由 sync.Once 保证只执行一次。
func loadCL100K() (tiktokenEncoder, error) {
	return tiktoken.GetEncoding("cl100k_base")
}
