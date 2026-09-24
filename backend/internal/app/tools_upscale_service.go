package app

import (
	"path/filepath"
	"strings"

	"infinite-canvas/backend/internal/model"
)

// ResourceLocalFilePath 本地存储资源的绝对路径；gateway 产物同机落库，超分直接读文件。
func (s *Service) ResourceLocalFilePath(userID string, resourceID string) (string, error) {
	resource, err := s.repo.ResourceForUser(userID, strings.TrimSpace(resourceID))
	if err != nil {
		return "", err
	}
	if resource.Status != model.ResourceStatusReady {
		return "", BadAuthRequest("资源尚未就绪：" + resourceID)
	}
	if resource.Provider != "local" {
		return "", BadAuthRequest("资源不在本地存储，无法直接读取：" + resourceID)
	}
	// 绝对路径：相对路径只在本进程 cwd 下有效，传给 Gateway（不同 cwd）就读不到了
	return filepath.Abs(filepath.Join(s.dataDir, "resources", filepath.FromSlash(resource.ObjectKey)))
}
