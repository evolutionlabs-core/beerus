package nip

type NameEnquiry struct {
	Code        string `json:"response_code"`
	Message     string `json:"message"`
	BankCode    string `json:"bank_code"`
	BankName    string `json:"bank_name,omitempty"`
	AccountNo   string `json:"account_number"`
	AccountName string `json:"account_name,omitempty"`
}

type BVNRequest struct {
	Name  string `json:"name"`
	Phone string `json:"phone"`
	DOB   string `json:"dob"`
}

type BVNReply struct {
	BVN   string `json:"bvn"`
	Phone string `json:"phone"`
}
