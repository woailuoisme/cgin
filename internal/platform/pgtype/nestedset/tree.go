package nestedset

// TreeNode 表示包含泛型业务实体与下属子节点列表的树形节点容器。
type TreeNode[T any] struct {
	// 业务实体数据载荷
	Data T `json:"data"`
	// 当前节点在树中的唯一 ltree 路径
	Path Path `json:"path"`
	// 下属子节点列表
	Children []*TreeNode[T] `json:"children,omitempty"`
}

// BuildTree 将一组包含 ltree Path 的平铺实体对象构建为层级嵌套的多叉树结构。
// 算法时间复杂度为 O(N)，自动兼容全量树构建与任意局部子树切片构建（找不到父节点的顶层节点自动提升为局部根节点）。
func BuildTree[T any](items []T, getPath func(T) Path) []*TreeNode[T] {
	if len(items) == 0 {
		return nil
	}

	// 1. 初始化所有节点的容器并构建快速索引表
	nodeMap := make(map[Path]*TreeNode[T], len(items))
	nodes := make([]*TreeNode[T], 0, len(items))

	for _, item := range items {
		p := getPath(item)
		node := &TreeNode[T]{
			Data:     item,
			Path:     p,
			Children: make([]*TreeNode[T], 0),
		}
		nodeMap[p] = node
		nodes = append(nodes, node)
	}

	// 2. 遍历节点，挂载到对应父节点或提升为根节点
	roots := make([]*TreeNode[T], 0)
	for _, node := range nodes {
		parentPath := node.Path.Parent()
		if parentNode, exists := nodeMap[parentPath]; exists {
			parentNode.Children = append(parentNode.Children, node)
		} else {
			roots = append(roots, node)
		}
	}

	return roots
}

// FlattenTree 将多叉嵌套树结构按照前序遍历方式重新扁平化为线性实体切片。
func FlattenTree[T any](roots []*TreeNode[T]) []T {
	if len(roots) == 0 {
		return nil
	}

	var result []T
	var traverse func(node *TreeNode[T])
	traverse = func(node *TreeNode[T]) {
		if node == nil {
			return
		}
		result = append(result, node.Data)
		for _, child := range node.Children {
			traverse(child)
		}
	}

	for _, root := range roots {
		traverse(root)
	}

	return result
}
