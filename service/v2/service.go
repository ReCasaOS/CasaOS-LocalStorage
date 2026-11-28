package v2

import (
	"sync"

	"github.com/ReCasaOS/CasaOS-LocalStorage/service/v2/wrapper"
	"gorm.io/gorm"
)

type LocalStorageService struct {
	_mountinfo   wrapper.MountInfoWrapper
	_db          *gorm.DB
	_mergeMu     sync.Mutex
	_mergeErrors map[string]string
	_setAsides   map[string]int // per mount point, how often its contents were set aside
}

func NewLocalStorageService(db *gorm.DB, mountinfo wrapper.MountInfoWrapper) *LocalStorageService {
	return &LocalStorageService{
		_mountinfo:   mountinfo,
		_db:          db,
		_mergeErrors: make(map[string]string),
		_setAsides:   make(map[string]int),
	}
}
