// main.go（唯一入口，仅负责调用其他函数）
package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	// 1. 初始化 gRPC 连接（调用 init.go 中的函数）
	InitGRPC()

	// 2. 优雅退出：监听 Ctrl+C 信号，关闭连接
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer CloseGRPC() // 程序退出前必执行 CloseGRPC

	// 3. 执行测试函数（按需注释/启用，控制执行逻辑）
	log.Println("\n===== 开始执行测试 =====")
	//TestGetBrandList()  // 品牌列表（必执行）
	// TestCreateBrand()  // 新增品牌（按需启用）
	// TestUpdateBrand()  // 更新品牌（按需启用）
	// TestDeleteBrand(1087)  // 删除品牌（谨慎启用，传参指定ID）
	TestGetCategoryBrandList()
	// 4. 等待退出信号（避免程序执行完直接退出，方便看日志）
	log.Println("\n===== 测试执行完成，按 Ctrl+C 退出 =====")
	<-sigCh
	log.Println("程序开始退出...")
}