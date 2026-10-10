package transaction

// Input returns the editable fields of a stored transaction as a CreateInput, so
// that a partial update can be applied on top and re-validated as a whole.
// Account is not editable (it is a denormalized snapshot of the balance name).
func (e Transaction) Input() CreateInput {
	to, cat := "", ""
	if e.ToBalanceUID != nil {
		to = *e.ToBalanceUID
	}
	if e.CategoryUID != nil {
		cat = *e.CategoryUID
	}
	return CreateInput{
		Type:         string(e.Type),
		ToBalanceUID: to,
		CategoryUID:  cat,
		Amount:       DecimalInput(e.Amount),
		Currency:     e.Currency,
		BalanceUID:   e.BalanceUID,
		SpentAt:      e.SpentAt,
		Note:         e.Note,
	}
}
