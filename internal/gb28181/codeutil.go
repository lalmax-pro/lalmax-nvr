package gb28181

import "regexp"

// 国标编号类型码（20位编号的第11-13位）
const (
	// GBTypeBusinessGroup 业务分组（顶层）
	GBTypeBusinessGroup = "215"
	// GBTypeVirtualOrg 虚拟组织
	GBTypeVirtualOrg = "216"
)

var gbCodeRe = regexp.MustCompile(`^\d{20}$`)

// GBCode 表示解析后的20位国标编号。
//
// 编号结构: 中心编码(8) + 行业编码(2) + 类型编码(3) + 网络标识(1) + 序号(6)
type GBCode struct {
	CenterCode   string // 中心编码, 由监控中心所在地行政区划代码确定 (GB/T 2260)
	IndustryCode string // 行业编码
	TypeCode     string // 类型编码, 215=业务分组 216=虚拟组织
	NetCode      string // 网络标识
	SN           string // 序号
}

// DecodeGBCode 解析20位国标编号, 非法返回 nil。
func DecodeGBCode(code string) *GBCode {
	if !gbCodeRe.MatchString(code) {
		return nil
	}
	return &GBCode{
		CenterCode:   code[0:8],
		IndustryCode: code[8:10],
		TypeCode:     code[10:13],
		NetCode:      code[13:14],
		SN:           code[14:20],
	}
}

// IsBusinessGroup 判断编号是否为业务分组(215)。
func IsBusinessGroup(code string) bool {
	c := DecodeGBCode(code)
	return c != nil && c.TypeCode == GBTypeBusinessGroup
}

// IsVirtualOrg 判断编号是否为虚拟组织(216)。
func IsVirtualOrg(code string) bool {
	c := DecodeGBCode(code)
	return c != nil && c.TypeCode == GBTypeVirtualOrg
}
