package apis

import "github.com/samuelncui/yatm/entity"

type mediaService struct {
	entity.UnimplementedMediaServiceServer
	api *API
}

type libraryService struct {
	entity.UnimplementedLibraryServiceServer
	api *API
}
