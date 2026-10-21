package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

type projectView struct {
	ID          string  `json:"id"`
	Key         string  `json:"key"`
	Title       string  `json:"title"`
	Description string  `json:"description"`
	Revision    int64   `json:"revision"`
	ArchivedAt  *string `json:"archivedAt"`
}

func resolveProject(ctx context.Context, client *apiClient, value string) (projectView, error) {
	// A Task Run knows its own Project from run.json, so that key needs no
	// list read — which the Run may not be admitted to make.
	if run := client.context.run; client.context.mode == modeAgent && run.ProjectKey != "" &&
		value == run.ProjectKey {
		value = run.ProjectID
	}
	if _, err := uuid.Parse(value); err == nil {
		var project projectView
		err := client.get(ctx, client.companyPath("projects/"+value), &project)
		return project, err
	}
	var page struct {
		Items []projectView `json:"items"`
	}
	if err := client.get(ctx, client.companyPath("projects")+"?include_archived=true", &page); err != nil {
		return projectView{}, err
	}
	for _, p := range page.Items {
		if p.Key == value {
			return p, nil
		}
	}
	return projectView{}, usagef("Project %q was not found", value)
}

func newProjectCmd() *cobra.Command {
	root := &cobra.Command{Use: "project", Short: "Manage Company projects"}
	for _, operation := range []string{"list", "get", "create", "update", "archive"} {
		name := operation
		cmd := &cobra.Command{
			Use:   name,
			Short: strings.ToUpper(name[:1]) + name[1:] + " projects",
			Args:  cobra.NoArgs,
		}
		if name == "get" || name == "update" || name == "archive" {
			cmd.Use += " <project-id|project-key>"
			cmd.Args = cobra.ExactArgs(1)
		}
		if name == "create" {
			cmd.Flags().String("key", "", "immutable Project key")
			_ = cmd.MarkFlagRequired("key")
		}
		if name == "create" || name == "update" {
			cmd.Flags().String("title", "", "Project title")
			addContentFlags(cmd, "description", "Project description")
		}
		if name == "create" {
			_ = cmd.MarkFlagRequired("title")
		}
		if name == "list" {
			cmd.Flags().Bool("include-archived", false, "include archived Projects")
		}
		cmd.RunE = func(cmd *cobra.Command, args []string) error {
			client, err := newAPIClient(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := apiContext()
			defer cancel()
			path := client.companyPath("projects")
			var raw json.RawMessage
			switch name {
			case "list":
				include, _ := cmd.Flags().GetBool("include-archived")
				if include {
					path += "?include_archived=true"
				}
				err = client.get(ctx, path, &raw)
			case "create":
				key, _ := cmd.Flags().GetString("key")
				title, _ := cmd.Flags().GetString("title")
				description, _, readErr := contentFlag(cmd, "description")
				if readErr != nil {
					return readErr
				}
				err = client.post(
					ctx,
					path,
					map[string]any{"key": key, "title": title, "description": description},
					&raw,
				)
			default:
				project, readErr := resolveProject(ctx, client, args[0])
				if readErr != nil {
					return relayRunScopeRefusal(readErr)
				}
				path += "/" + project.ID
				switch name {
				case "get":
					err = client.get(ctx, path, &raw)
				case "archive":
					err = client.post(
						ctx,
						path+"/archive",
						map[string]any{"expectedRevision": project.Revision},
						&raw,
					)
				case "update":
					body := map[string]any{"expectedRevision": project.Revision}
					if cmd.Flags().Changed("title") {
						title, _ := cmd.Flags().GetString("title")
						body["title"] = title
					}
					description, provided, readErr := contentFlag(cmd, "description")
					if readErr != nil {
						return readErr
					}
					if provided {
						body["description"] = description
					}
					err = client.patch(ctx, path, body, &raw)
				}
			}
			if err != nil {
				return relayRunScopeRefusal(err)
			}
			return printResult(cmd, raw, func(out io.Writer) error {
				if name == "list" {
					var page struct {
						Items []projectView `json:"items"`
					}
					if err := json.Unmarshal(raw, &page); err != nil {
						return err
					}
					rows := [][]string{}
					for _, p := range page.Items {
						status := "active"
						if p.ArchivedAt != nil {
							status = "archived"
						}
						rows = append(rows, []string{p.Key, p.Title, status, p.ID})
					}
					printTable(out, []string{"KEY", "TITLE", "STATE", "ID"}, rows)
					return nil
				}
				var p projectView
				if err := json.Unmarshal(raw, &p); err != nil {
					return err
				}
				_, err := fmt.Fprintf(
					out,
					"%s — %s\n%s\nID: %s\nRevision: %d\n",
					p.Key,
					p.Title,
					p.Description,
					p.ID,
					p.Revision,
				)
				return err
			})
		}
		root.AddCommand(cmd)
	}
	return root
}
