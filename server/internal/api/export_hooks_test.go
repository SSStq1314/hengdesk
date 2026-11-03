package api

// SetEgressExportMax 测试用：临时调小导出上限。
func SetEgressExportMax(n int) (restore func()) {
	old := egressExportMax
	egressExportMax = n
	return func() { egressExportMax = old }
}
