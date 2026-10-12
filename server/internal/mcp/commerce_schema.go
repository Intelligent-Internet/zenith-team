package mcp

import "encoding/json"

func commerceManifestSubmitInputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type":"object",
		"additionalProperties":false,
		"required":["environment","manifest"],
		"properties":{
			"environment":{
				"type":"string",
				"enum":["sandbox","live"],
				"description":"Zenith Team Commerce environment explicitly selected by the user."
			},
			"manifest":{
				"type":"object",
				"additionalProperties":false,
				"required":["schemaVersion","site","offers"],
				"properties":{
					"schemaVersion":{
						"type":"integer",
						"const":2,
						"description":"Closed Zenith Team Commerce manifest schema version."
					},
					"site":{
						"type":"object",
						"additionalProperties":false,
						"required":["alias","origin","portalReturnPath"],
						"properties":{
							"alias":{
								"type":"string",
								"pattern":"^[a-z][a-z0-9_-]{1,63}$",
								"description":"Stable site alias."
							},
							"origin":{
								"type":"string",
								"minLength":1,
								"maxLength":2048,
								"description":"Canonical HTTPS origin, or an exact HTTP loopback origin for local sandbox development."
							},
							"portalReturnPath":{
								"type":"string",
								"minLength":1,
								"maxLength":1024,
								"pattern":"^/([^/]|$)",
								"description":"Relative site path used after Customer Portal; absolute URLs are forbidden."
							}
						}
					},
					"offers":{
						"type":"array",
						"minItems":1,
						"maxItems":100,
						"items":{
							"type":"object",
							"additionalProperties":false,
							"required":["alias","displayName","type","entitlement","amount","currency","automaticTax","successPath","cancelPath"],
							"properties":{
								"alias":{
									"type":"string",
									"pattern":"^[a-z][a-z0-9_-]{1,63}$",
									"description":"Stable offer alias used by checkout(offerAlias)."
								},
								"displayName":{"type":"string","minLength":1,"maxLength":255},
								"type":{
									"type":"string",
									"enum":["one_time","subscription"],
									"description":"one_time forbids planGroup and interval; subscription requires both."
								},
								"planGroup":{
									"type":"string",
									"pattern":"^[a-z][a-z0-9_-]{1,63}$",
									"description":"Subscription-only product group shared by immutable monthly and yearly Prices."
								},
								"entitlement":{
									"type":"string",
									"pattern":"^[a-z][a-z0-9_.:-]{1,127}$"
								},
								"amount":{
									"type":"integer",
									"minimum":1,
									"maximum":999999999999,
									"description":"Fixed quantity-1 price in currency minor units."
								},
								"currency":{"type":"string","pattern":"^[a-z]{3}$"},
								"interval":{
									"type":"string",
									"enum":["month","year"],
									"description":"Subscription-only billing interval. Omit for one_time offers."
								},
								"automaticTax":{"type":"boolean"},
								"successPath":{
									"type":"string",
									"minLength":1,
									"maxLength":1024,
									"pattern":"^/([^/]|$)",
									"description":"Relative processing-page path; redirects never grant access."
								},
								"cancelPath":{
									"type":"string",
									"minLength":1,
									"maxLength":1024,
									"pattern":"^/([^/]|$)",
									"description":"Relative cancellation path."
								}
							},
							"allOf":[
								{
									"if":{"properties":{"type":{"const":"subscription"}},"required":["type"]},
									"then":{"required":["planGroup","interval"]}
								},
								{
									"if":{"properties":{"type":{"const":"one_time"}},"required":["type"]},
									"then":{"not":{"anyOf":[{"required":["planGroup"]},{"required":["interval"]}]}}
								}
							]
						}
					}
				}
			}
		}
	}`)
}
