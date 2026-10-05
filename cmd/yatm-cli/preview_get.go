package main

import (
	"context"
	"encoding/hex"
	"fmt"

	"github.com/samuelncui/yatm/entity"
)

type previewCapabilitiesCommand struct{ runtime *runtime }

func (c *previewCapabilitiesCommand) Execute(_ []string) error {
	// Probe generation dependencies independently of reading existing derivative assets.
	ctx, cancel := c.runtime.context()
	defer cancel()
	reply, err := callRPC[entity.GetPreviewCapabilitiesRequest, entity.GetPreviewCapabilitiesResponse](ctx, c.runtime,
		entity.PreviewService_GetCapabilities_FullMethodName, &entity.GetPreviewCapabilitiesRequest{})
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}

type previewGetCommand struct {
	runtime *runtime
	entryOptions
	VersionID *int64 `long:"version-id" description:"Read Preview for this saved version instead of a current entry"`
	Signature string `long:"signature" description:"Read Preview for this content signature, hexadecimal; exclusive with an entry or version"`
}

func (c *previewGetCommand) Execute(_ []string) error {
	// Preview storage is content-addressed, so the request carries the signature the operator
	// named, whichever identity they used to name it.
	ctx, cancel := c.runtime.context()
	defer cancel()
	signature, err := c.contentSignature(ctx)
	if err != nil {
		return err
	}
	reply, err := callRPC[entity.GetPreviewRequest, entity.GetPreviewResponse](ctx, c.runtime,
		entity.PreviewService_Get_FullMethodName, &entity.GetPreviewRequest{Signature: signature})
	if err != nil {
		return err
	}
	return writeProto(c.runtime.stdout, reply)
}

// contentSignature resolves one named content identity: an explicit signature, a saved version, or
// a current entry whose detail publishes the identity of its original.
func (c *previewGetCommand) contentSignature(ctx context.Context) ([]byte, error) {
	entry := c.FileID != nil || c.LocationID != nil || c.Path != ""
	named := 0
	for _, present := range []bool{c.Signature != "", c.VersionID != nil, entry} {
		if present {
			named++
		}
	}
	if named != 1 {
		return nil, usageError(fmt.Errorf("choose exactly one of signature, version-id or an entry"))
	}
	if c.Signature != "" {
		value, err := hex.DecodeString(c.Signature)
		if err != nil || len(value) == 0 {
			return nil, usageError(fmt.Errorf("signature must be nonempty hexadecimal"))
		}
		return value, nil
	}
	if c.VersionID != nil {
		if err := positiveID("FileVersion ID", *c.VersionID); err != nil {
			return nil, err
		}
		reply, err := callRPC[entity.GetFileVersionRequest, entity.GetFileVersionResponse](ctx, c.runtime,
			entity.FilesService_GetVersion_FullMethodName, &entity.GetFileVersionRequest{Id: *c.VersionID})
		if err != nil {
			return nil, err
		}
		if len(reply.GetVersion().GetSignature()) == 0 {
			return nil, fmt.Errorf("this saved version records no content signature")
		}
		return reply.GetVersion().GetSignature(), nil
	}
	ref, err := c.reference()
	if err != nil {
		return nil, err
	}
	detail, err := getFilesEntry(ctx, c.runtime, ref)
	if err != nil {
		return nil, err
	}
	// A current original has an identity only while its observation applies; anything else is
	// read as a saved version or named by its signature directly.
	if len(detail.GetContentSignature()) == 0 {
		return nil, fmt.Errorf("this entry has no applicable current content signature; use --version-id or --signature")
	}
	return detail.GetContentSignature(), nil
}
