package tools

import "tars/internal/config"

func Default(cfg *config.Config) *Registry {
	r := NewRegistry()
	r.Register(execTool())
	r.Register(readFileTool())
	r.Register(writeFileTool())
	r.Register(editFileTool())
	r.Register(grepTool())
	r.Register(globTool())
	r.Register(lsTool())
	r.Register(webfetchTool())
	r.Register(websearchTool())
	r.Register(memoryStoreTool())
	r.Register(memoryQueryTool())
	r.Register(taskDoneTool())
	r.Register(taskTool())
	r.Register(applyPatchTool())
	r.Register(contextTool())
	return r
}
