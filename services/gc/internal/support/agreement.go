package support

import "encoding/json"

// Steam's numeric agreement fields can arrive as JSON numbers or strings.
func (a *Agreement) UnmarshalJSON(raw []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	values := map[string]*string{"agreementid": &a.ID, "itemid": &a.ItemID, "status": &a.Status, "period": &a.Period, "recurringamt": &a.Amount, "currency": &a.Currency, "nextpayment": &a.NextPayment}
	for key, target := range values {
		if value, ok := fields[key]; ok {
			if len(value) > 0 && value[0] == '"' {
				if err := json.Unmarshal(value, target); err != nil {
					return err
				}
			} else {
				var n json.Number
				if err := json.Unmarshal(value, &n); err != nil {
					return err
				}
				*target = n.String()
			}
		}
	}
	return nil
}
