package nip

type BVNRequest struct {
	Name  string `json:"name"`
	Phone string `json:"phone"`
	DOB   string `json:"dob"`
}

type BVNReply struct {
	BVN   string `json:"bvn"`
	Phone string `json:"phone"`
}
