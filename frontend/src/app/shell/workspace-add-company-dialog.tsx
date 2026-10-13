import { useRef, useState } from "react";
import { useNavigate } from "react-router";
import { JoinCompanyForm } from "@/features/company-invitations/components/join-company-form";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/shared/ui/core/dialog";
import { ArrowRight, Building2, Users } from "@/shared/ui/icons";
import "./workspace-add-company-dialog.css";

export function WorkspaceAddCompanyDialog({
  open,
  onOpenChange,
  onRestoreFocus,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onRestoreFocus: () => void;
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        className="company-join-dialog"
        overlayClassName="company-join-overlay"
        onCloseAutoFocus={(event) => {
          event.preventDefault();
          onRestoreFocus();
        }}
      >
        <AddCompanyChoices onClose={() => onOpenChange(false)} />
      </DialogContent>
    </Dialog>
  );
}

function AddCompanyChoices({ onClose }: { onClose: () => void }) {
  const [joining, setJoining] = useState(false);
  const restoreJoinFocus = useRef(false);
  const navigate = useNavigate();

  return (
    <>
      <DialogHeader className="text-left">
        <DialogTitle>
          {joining ? "Join an existing company" : "Add company"}
        </DialogTitle>
        <DialogDescription className="text-secondary leading-5">
          {joining
            ? "Paste the invite link your team shared with you. You’ll review the company and your role before joining."
            : "Create a new company or join a team with an invite link."}
        </DialogDescription>
      </DialogHeader>
      {joining ? (
        <JoinCompanyForm
          onBack={() => {
            restoreJoinFocus.current = true;
            setJoining(false);
          }}
          onContinue={onClose}
        />
      ) : (
        <div className="grid gap-[10px]">
          <button
            type="button"
            className="workspace-add-company-choice"
            onClick={() => {
              onClose();
              navigate("/companies/new");
            }}
          >
            <Building2 size={22} aria-hidden="true" />
            <span>
              <strong>Create a new company</strong>
              <span>Build a new workspace with your AI team.</span>
            </span>
            <ArrowRight size={18} aria-hidden="true" />
          </button>
          <button
            ref={(node) => {
              if (node && restoreJoinFocus.current) {
                node.focus();
                restoreJoinFocus.current = false;
              }
            }}
            type="button"
            className="workspace-add-company-choice"
            onClick={() => setJoining(true)}
          >
            <Users size={22} aria-hidden="true" />
            <span>
              <strong>Join an existing company</strong>
              <span>Use an invite link to join your team.</span>
            </span>
            <ArrowRight size={18} aria-hidden="true" />
          </button>
        </div>
      )}
    </>
  );
}
