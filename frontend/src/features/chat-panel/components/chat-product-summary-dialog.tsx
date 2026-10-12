import {
  Dialog,
  DialogContent as CoreDialogContent,
  DialogDescription,
  DialogTitle,
} from "@/shared/ui/core/dialog";
import type { ProductSummary } from "@/features/chat-panel/model/discovery-product-summary";
import type { GeneratedImagePreview } from "@/features/chat-panel/model/generated-image-assets";
import { GlassDivider } from "@/shared/ui/glass-divider";
import { GradientCtaButton } from "@/shared/ui/gradient-cta-button";
import { Icon } from "@/shared/ui/icon";

type ChatProductSummaryDialogProps = {
  image?: GeneratedImagePreview;
  open: boolean;
  summary: ProductSummary;
  onConfirm: () => void;
  onEdit: () => void;
};

export function ChatProductSummaryDialog({
  image,
  open,
  summary,
  onConfirm,
  onEdit,
}: ChatProductSummaryDialogProps) {
  return (
    <Dialog open={open}>
      <CoreDialogContent
        className="!fixed !left-1/2 !top-1/2 !z-[70] !w-[min(1240px,calc(100vw-48px))] !max-w-[min(1240px,calc(100vw-48px))] !-translate-x-1/2 !-translate-y-1/2 !border-0 !bg-transparent !p-0 !shadow-none !outline-none max-[640px]:!w-[calc(100vw-24px)] max-[640px]:!max-w-[calc(100vw-24px)]"
        overlayClassName="!fixed !inset-0 !z-[68] !bg-[#212121]/[0.95]"
        showCloseButton={false}
      >
        <DialogTitle className="sr-only">
          Review your business context
        </DialogTitle>
        <DialogDescription className="sr-only">
          CEO Agent will use this business snapshot to generate your first
          execution plan.
        </DialogDescription>
        <div className="pointer-events-auto flex h-[min(800px,calc(100dvh-48px))] w-full flex-col overflow-hidden rounded-[20px] border border-white/24 bg-[#252827]/88 text-white shadow-[0_32px_110px_rgba(0,0,0,0.58),inset_0_1px_0_rgba(255,255,255,0.16)] backdrop-blur-xl max-[640px]:h-[min(760px,calc(100dvh-24px))]">
          <div className="flex shrink-0 items-start justify-between gap-5 px-6 pb-4 pt-5 max-[640px]:gap-3 max-[640px]:px-4 max-[640px]:pt-4">
            <div className="min-w-0">
              <p className="m-0 text-[12px] font-bold uppercase tracking-[0.22em] text-[#f0d19b]">
                Your business context is ready
              </p>
              <h2 className="m-0 mt-2 text-[28px] font-bold leading-tight text-white max-[640px]:text-[22px]">
                Review your launch brief
              </h2>
              <p className="m-0 mt-1.5 text-[13px] font-medium leading-5 text-white/68">
                Review the brief before Zenith Team briefs your Agents and
                builds your first roadmap.
              </p>
            </div>
            <button
              type="button"
              className="grid size-10 shrink-0 place-items-center rounded-xl border border-white/18 bg-white/8 text-white transition hover:bg-white/12"
              aria-label="Edit product context"
              onClick={onEdit}
            >
              <Icon name="edit-2" className="size-5" />
            </button>
          </div>
          <GlassDivider className="opacity-[0.18]" />

          <div className="grid min-h-0 flex-1 grid-cols-[minmax(0,1.08fr)_minmax(360px,0.92fr)] gap-5 overflow-y-auto px-6 py-5 [scrollbar-width:none] max-[980px]:grid-cols-1 max-[640px]:gap-4 max-[640px]:px-4 max-[640px]:py-4 [&::-webkit-scrollbar]:hidden">
            <section className="sticky min-w-0 self-start max-[980px]:static">
              <div className="overflow-hidden rounded-2xl border border-white/18 bg-white/[0.055] shadow-[inset_0_1px_0_rgba(255,255,255,0.14),0_18px_46px_rgba(0,0,0,0.22)] backdrop-blur-md">
                {image ? (
                  <img
                    className="block aspect-[3/2] h-auto w-full object-cover object-center"
                    src={image.url}
                    alt={image.alt}
                    draggable={false}
                    height={image.height}
                    width={image.width}
                  />
                ) : (
                  <div className="grid aspect-[3/2] place-items-center bg-white/[0.045] px-8 text-center">
                    <div className="grid justify-items-center gap-3">
                      <Icon name="artboard" className="size-9 text-white/46" />
                      <p className="m-0 max-w-[320px] text-sm font-medium leading-6 text-white/58">
                        Product visual will appear here after image generation.
                      </p>
                    </div>
                  </div>
                )}
              </div>
            </section>

            <section className="grid min-w-0 content-start gap-3">
              <SummaryRow label="Company" value={summary.company} />
              <SummaryRow label="Product & wedge" value={summary.product} />
              <SummaryRow label="ICP" value={summary.audience} />
              <SummaryRow
                label="Not serving in v1"
                value={summary.excludedAudience}
              />
              <div className="grid gap-3 sm:grid-cols-2">
                <SummaryRow
                  label="Sales & payment"
                  value={summary.salesAndPayment}
                />
                <SummaryRow label="Fulfillment" value={summary.fulfillment} />
              </div>
              <div className="grid gap-3 sm:grid-cols-2">
                <SummaryRow
                  label="Revenue model"
                  value={summary.businessModel}
                />
                <SummaryRow label="Current stage" value={summary.stage} />
              </div>
              <SummaryRow
                label="Mission & values"
                value={summary.missionAndValues}
              />
              <SummaryRow label="GTM v1" value={summary.gtm} />
              <SummaryRow label="CEO Recommendation" value={summary.focus} />
            </section>
          </div>

          <GlassDivider className="opacity-[0.18]" />
          <div className="flex shrink-0 items-center gap-3 px-6 py-5 max-[640px]:flex-col max-[640px]:items-stretch max-[640px]:px-4 max-[640px]:py-4">
            <GradientCtaButton
              hideIcon
              size="auto"
              className="h-12 !w-auto rounded-xl !px-6 text-sm font-bold max-[640px]:!w-full"
              type="button"
              onClick={onConfirm}
            >
              Save &amp; build roadmap
            </GradientCtaButton>
            <button
              type="button"
              className="h-12 rounded-xl border border-white/24 bg-white/[0.06] px-5 text-sm font-medium text-white/82 transition hover:bg-white/10 hover:text-white max-[640px]:w-full"
              onClick={onEdit}
            >
              I want to edit context
            </button>
          </div>
        </div>
      </CoreDialogContent>
    </Dialog>
  );
}

function SummaryRow({ label, value }: { label: string; value: string }) {
  return (
    <div className="rounded-xl border border-white/20 bg-white/[0.07] px-4 py-3 shadow-[inset_0_1px_0_rgba(255,255,255,0.12),0_10px_24px_rgba(0,0,0,0.12)] backdrop-blur-md">
      <p className="m-0 text-[11px] font-bold uppercase tracking-[0.12em] text-white/42">
        {label}
      </p>
      <p className="m-0 mt-1 text-sm font-medium leading-6 text-white/88 wrap-break-word">
        {value}
      </p>
    </div>
  );
}
