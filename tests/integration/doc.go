// Package integration 本身不含代码，存在的唯一目的是让 ./tests/integration 下
// 全部带 integration 构建标签的用例所属的包真实存在。
// 没有它，go test ./... 会因为"构建约束排除了目录下所有文件"而直接失败。
package integration
