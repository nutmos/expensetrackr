package transaction

import "k8s.io/utils/ptr"

// Input returns the editable fields of a stored transaction as a CreateInput, so
// that a partial update can be applied on top and re-validated as a whole.
// Account is not editable (it is a denormalized snapshot of the balance name).
func (e Transaction) Input() CreateInput {
	return CreateInput{
		Type:         string(e.Type),
		ToBalanceUID: ptr.Deref(e.ToBalanceUID, ""),
		CategoryUID:  ptr.Deref(e.CategoryUID, ""),
		Amount:       DecimalInput(e.Amount),
		Currency:     e.Currency,
		BalanceUID:   e.BalanceUID,
		SpentAt:      e.SpentAt,
		Note:         e.Note,
	}
}
