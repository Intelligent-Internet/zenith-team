package mcp

import "encoding/json"

func askHumanInputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type":"object",
		"additionalProperties":false,
		"required":["title","prompts"],
		"properties":{
			"title":{"type":"string"},
			"prompts":{
				"type":"array",
				"items":{
					"type":"object",
					"additionalProperties":false,
					"required":["id","prompt","selectionMode","required"],
					"properties":{
						"id":{"type":"string"},
						"prompt":{"type":"string"},
						"toolSetup":{"type":"string","enum":["zenith-studio"],"description":"Request Zenith Studio setup when disabled or needing connection. Use exactly one required single prompt without options or allowCustomAnswer. Omit this field for model selection. This requests human setup; it does not enable tools for this Run."},
						"socialPost":{
							"type":"object","additionalProperties":false,"required":["connectionId","targetId","text"],
							"description":"Required before any social_publish. Shows the human the exact account, caption and media. Use one required single prompt, allowCustomAnswer true and options in this order: publish (Publish now), cancel (Do not publish). End the Run after asking. Only call social_publish with the returned questionId after the human selects publish; a custom answer means revise and ask again.",
							"properties":{
								"connectionId":{"type":"string","format":"uuid"},
								"targetId":{"type":"string","format":"uuid"},
								"text":{"type":"string","maxLength":63206},
								"attachmentIds":{"type":"array","maxItems":4,"items":{"type":"string","format":"uuid"},"description":"Exact image or video IDs from social_read in this conversation. X: up to four images OR one MP4. Other supported platforms: one image. Never mix video and images."}
							}
						},
						"helpText":{"type":["string","null"]},
						"selectionMode":{
							"type":"string",
							"enum":["single","multi"],
							"description":"Allowed values are single and multi. A free text prompt uses single with options omitted or empty."
						},
						"required":{"type":"boolean"},
						"allowCustomAnswer":{"type":"boolean","description":"For a prompt with options, show an Other choice with a free-text field."},
						"options":{
							"type":"array",
							"description":"Omit or pass an empty array for free text. Otherwise provide two to twenty choices.",
							"items":{
								"type":"object",
								"additionalProperties":false,
								"required":["id","label"],
								"properties":{
									"id":{"type":"string"},
									"label":{"type":"string"}
								}
							}
						}
					}
				}
			}
		}
	}`)
}
