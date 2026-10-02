package fence

// posting 是倒排表中的一条记录。
type posting struct {
	doc uint32 // 内部文档序号
	tf  uint32
}

// folderNode 是目录树节点。
type folderNode struct {
	id       int     // 外部目录ID
	parent   int32   // 父节点内部序号，-1 表示根
	children []int32 // 子节点内部序号
	docs     []int32 // 直接挂在该目录下的文档内部序号
	subDocs  int     // 子树文档总数（装载后静态）
}

// userState 是单个用户的授权与可见性缓存。
type userState struct {
	grants     []int32  // 授权的目录内部序号
	visibleCnt int      // 可见文档总数
	vis        []uint64 // 目录可见性位图，按内部目录序号索引
}
