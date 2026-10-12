package protocol

import (
	"errors"
	"strings"
)

const supportedPhoneCountryISO2 = "|AF|AL|DZ|AS|AD|AO|AI|AG|AR|AM|AW|AU|AT|AZ|BS|BH|BD|BB|BY|BE|BZ|BJ|BM|BT|BO|BA|BW|BR|IO|VG|BN|BG|BF|BI|KH|CM|CA|CV|BQ|KY|CF|TD|CL|CN|CX|CC|CO|KM|CG|CK|CR|HR|CU|CW|CY|CZ|CD|DK|DJ|DM|DO|EC|EG|SV|GQ|ER|EE|SZ|ET|FK|FO|FJ|FI|FR|GF|PF|GA|GM|GE|DE|GH|GI|GR|GL|GD|GP|GU|GT|GG|GN|GW|GY|HT|HN|HK|HU|IS|IN|ID|IR|IQ|IE|IM|IL|IT|CI|JM|JP|JE|JO|KZ|KE|KI|XK|KW|KG|LA|LV|LB|LS|LR|LY|LI|LT|LU|MO|MG|MW|MY|MV|ML|MT|MH|MQ|MR|MU|YT|MX|FM|MD|MC|MN|ME|MS|MA|MZ|MM|NA|NR|NP|NL|NC|NZ|NI|NE|NG|NU|NF|KP|MK|MP|NO|OM|PK|PW|PS|PA|PG|PY|PE|PH|PL|PT|PR|QA|RO|RU|RW|RE|BL|SH|KN|LC|MF|PM|VC|WS|SM|SA|SN|RS|SC|SL|SG|SX|SK|SI|SB|SO|ZA|KR|SS|ES|LK|SD|SR|SJ|SE|CH|SY|ST|TW|TJ|TZ|TH|TL|TG|TK|TO|TT|TN|TM|TC|TV|TR|UG|UA|AE|GB|US|VI|UY|UZ|VU|VA|VE|VN|WF|EH|YE|ZM|ZW|AX|"

func NormalizePhone(countryInput, numberInput *string) (*string, *string, error) {
	if countryInput == nil && numberInput == nil {
		return nil, nil, nil
	}
	if countryInput == nil || numberInput == nil {
		return nil, nil, errors.New("phone country and number must be provided together")
	}
	country := strings.ToUpper(TrimContractWhitespace(*countryInput))
	number := TrimContractWhitespace(*numberInput)
	if len(country) != 2 || !strings.Contains(supportedPhoneCountryISO2, "|"+country+"|") {
		return nil, nil, errors.New("phone country must be a supported ISO-2 code")
	}
	if len(number) < 1 || len(number) > 15 {
		return nil, nil, errors.New("phone number must contain 1–15 digits")
	}
	for _, character := range number {
		if character < '0' || character > '9' {
			return nil, nil, errors.New("phone number must contain only digits")
		}
	}
	return &country, &number, nil
}
