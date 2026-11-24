package yunzhijia

// Status 是员工列表接口的 status 查询值。
type Status string

const (
	// StatusActive 表示在职员工。
	StatusActive Status = "active"
	// StatusInactive 表示离职员工。
	StatusInactive Status = "inactive"
)

// Employee 是员工列表里的一条记录。Mobile 可以为空。
type Employee struct {
	OpenID     string
	Name       string
	Department string
	Mobile     string
}

// EmployeePage 是员工列表的一页。Page/PageSize/Total 来自上游 pagination。
type EmployeePage struct {
	List     []Employee
	Page     int
	PageSize int
	Total    int
}

// ticketResponse 是 queryUserName 接口的响应体。
type ticketResponse struct {
	Msg  string `json:"msg"`
	Code int    `json:"code"`
	Data string `json:"data"`
}

// employeeListResponse 是员工列表接口的响应体。
type employeeListResponse struct {
	List       []employeeRecord `json:"list"`
	Pagination pagination       `json:"pagination"`
}

type employeeRecord struct {
	OpenID     string `json:"open_id"`
	Name       string `json:"name"`
	Department string `json:"department"`
	Mobile     string `json:"mobile"`
}

type pagination struct {
	Page     int `json:"page"`
	PageSize int `json:"page_size"`
	Total    int `json:"total"`
}
