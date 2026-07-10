package batch

import (
	"context"
	"fmt"
)

// ItemTemplate is a saved item field set (tasks/069): applied it pre-fills
// the item form; its barcode prefix seeds bulk add's auto-incrementing
// pattern. Personal or library-shared on the macros sharing model.
type ItemTemplate struct {
	OwnedMeta
	CallNumber string `json:"callNumber,omitempty"`
	Location   string `json:"location,omitempty"`
	Note       string `json:"note,omitempty"`
	// BarcodePrefix seeds bulk add ("B-" -> B-0001, B-0002, ...).
	BarcodePrefix string `json:"barcodePrefix,omitempty"`
	// BarcodeWidth is the zero-padded counter width (default 4).
	BarcodeWidth int `json:"barcodeWidth,omitempty"`
}

// itemTemplateKind wires ItemTemplate into the generic owned/shared CRUD
// engine.
var itemTemplateKind = ownedKind[ItemTemplate]{
	pk: "ITMPL#", sk: "T#",
	validate: validateItemTemplate,
	meta:     func(t *ItemTemplate) *OwnedMeta { return &t.OwnedMeta },
}

// CreateItemTemplate validates and stores a template for owner (in the
// shared partition when t.Shared). The id is minted server-side.
func (s *Service) CreateItemTemplate(ctx context.Context, t ItemTemplate, owner string) (ItemTemplate, error) {
	return createOwned(ctx, s.DB, itemTemplateKind, t, owner)
}

// UpdateItemTemplate replaces a template's definition. The owner may update; an
// admin may update a shared one. Flipping Shared moves it between partitions.
func (s *Service) UpdateItemTemplate(ctx context.Context, id string, t ItemTemplate, owner string, isAdmin bool) (ItemTemplate, error) {
	return updateOwned(ctx, s.DB, itemTemplateKind, id, t, owner, isAdmin)
}

// DeleteItemTemplate removes a template. The owner may delete; an admin may
// delete a shared one (tasks/292).
func (s *Service) DeleteItemTemplate(ctx context.Context, owner, id string, isAdmin bool) error {
	return deleteOwned(ctx, s.DB, itemTemplateKind, owner, id, isAdmin)
}

// GetItemTemplate resolves a template the caller can use: their own, or a
// shared one.
func (s *Service) GetItemTemplate(ctx context.Context, owner, id string) (ItemTemplate, error) {
	return getOwned(ctx, s.DB, itemTemplateKind, owner, id)
}

// ListItemTemplates returns the caller's templates plus every shared one,
// sorted by label.
func (s *Service) ListItemTemplates(ctx context.Context, owner string) ([]ItemTemplate, error) {
	return listOwned(ctx, s.DB, itemTemplateKind, owner)
}

func validateItemTemplate(t ItemTemplate) error {
	if t.Label == "" {
		return fmt.Errorf("%w: an item template needs a label", ErrValidation)
	}
	if t.BarcodeWidth < 0 || t.BarcodeWidth > 12 {
		return fmt.Errorf("%w: barcode width must be 0-12", ErrValidation)
	}
	return nil
}
