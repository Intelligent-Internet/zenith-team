import { useId } from "react";
import productBody from "./assets/fold-lyra/body.webp";
import productEyes from "./assets/fold-lyra/eyes.webp";
import productMouth from "./assets/fold-lyra/mouth.webp";
import productLeft from "./assets/fold-lyra/left.webp";
import productRight from "./assets/fold-lyra/right.webp";
import productFeet from "./assets/fold-lyra/feet.webp";
import ceoBody from "./assets/fold-andrew/body.webp";
import ceoEyes from "./assets/fold-andrew/eyes.webp";
import ceoMouth from "./assets/fold-andrew/mouth.webp";
import ceoLeft from "./assets/fold-andrew/left.webp";
import ceoRight from "./assets/fold-andrew/right.webp";
import ceoFeet from "./assets/fold-andrew/feet.webp";
import marketingBody from "./assets/fold-susan/body.webp";
import marketingEyes from "./assets/fold-susan/eyes.webp";
import marketingMouth from "./assets/fold-susan/mouth.webp";
import marketingLeft from "./assets/fold-susan/left.webp";
import marketingRight from "./assets/fold-susan/right.webp";
import marketingFeet from "./assets/fold-susan/feet.webp";
import operationsBody from "./assets/fold-ariana/body.webp";
import operationsEyes from "./assets/fold-ariana/eyes.webp";
import operationsMouth from "./assets/fold-ariana/mouth.webp";
import operationsLeft from "./assets/fold-ariana/left.webp";
import operationsRight from "./assets/fold-ariana/right.webp";
import operationsFeet from "./assets/fold-ariana/feet.webp";
import "./fold-avatar-team.css";

export type FoldTeamAppearance = "product" | "ceo" | "marketing" | "operations";
const artwork = {
  product: {
    body: productBody,
    eyes: productEyes,
    mouth: productMouth,
    left: productLeft,
    right: productRight,
    feet: productFeet,
  },
  ceo: {
    body: ceoBody,
    eyes: ceoEyes,
    mouth: ceoMouth,
    left: ceoLeft,
    right: ceoRight,
    feet: ceoFeet,
  },
  marketing: {
    body: marketingBody,
    eyes: marketingEyes,
    mouth: marketingMouth,
    left: marketingLeft,
    right: marketingRight,
    feet: marketingFeet,
  },
  operations: {
    body: operationsBody,
    eyes: operationsEyes,
    mouth: operationsMouth,
    left: operationsLeft,
    right: operationsRight,
    feet: operationsFeet,
  },
};
type Placement = readonly [number, number, number, number];
const poses: Record<FoldTeamAppearance, { left: Placement; right: Placement }> =
  {
    product: { left: [63, 140, 83, 40], right: [152, 121, 82, 64] },
    ceo: { left: [17, 106, 79, 49], right: [160, 113, 70, 57] },
    marketing: { left: [4, 99, 77, 66], right: [181, 105, 54, 44] },
    operations: { left: [22, 101, 72, 76], right: [157, 119, 78, 75] },
  };
function Part({ src, rect }: { src: string; rect: Placement }) {
  return (
    <image
      href={src}
      x={rect[0]}
      y={rect[1]}
      width={rect[2]}
      height={rect[3]}
    />
  );
}
export function FoldAvatarTeam({
  appearance,
}: {
  appearance: FoldTeamAppearance;
}) {
  const id = useId().replace(/:/g, "");
  const parts = artwork[appearance];
  const rearLeft = appearance === "ceo" || appearance === "marketing";
  const pose = poses[appearance];
  return (
    <svg
      className="fold-avatar__svg fold-team"
      data-appearance={appearance}
      viewBox="0 0 240 240"
      fill="none"
      aria-hidden="true"
      focusable="false"
    >
      <defs>
        <linearGradient id={`${id}-shoulder-fade`} x1="0" x2="1">
          <stop offset="0" stopColor="black" />
          <stop offset=".16" stopColor="white" />
          <stop offset="1" stopColor="white" />
        </linearGradient>
        <mask
          id={`${id}-shoulder`}
          maskUnits="userSpaceOnUse"
          x={pose.left[0]}
          y={pose.left[1] - 4}
          width={pose.left[2]}
          height={pose.left[3] + 8}
        >
          <rect
            x={pose.left[0]}
            y={pose.left[1] - 4}
            width={pose.left[2]}
            height={pose.left[3] + 8}
            fill={`url(#${id}-shoulder-fade)`}
          />
        </mask>
      </defs>
      <ellipse cx="130" cy="211" rx="68" ry="5" fill="#171923" opacity=".12" />
      <Part src={parts.feet} rect={[80, 161, 120, 52]} />
      <g className="fold-team__body">
        {rearLeft ? (
          <g className="fold-team__left">
            <Part src={parts.left} rect={pose.left} />
          </g>
        ) : null}
        {appearance === "marketing" ? (
          <g className="fold-team__right">
            <Part src={parts.right} rect={pose.right} />
          </g>
        ) : null}
        <Part src={parts.body} rect={[58, 35, 148, 147]} />
        <g className="fold-team__eyes">
          <Part src={parts.eyes} rect={[92, 77, 80, 46]} />
        </g>
        <Part src={parts.mouth} rect={[119, 124, 30, 16]} />
        {appearance !== "marketing" ? (
          <g className="fold-team__right">
            <Part src={parts.right} rect={pose.right} />
          </g>
        ) : null}
        {!rearLeft ? (
          <g className="fold-team__left">
            <g
              mask={
                appearance === "product" ? `url(#${id}-shoulder)` : undefined
              }
            >
              <Part src={parts.left} rect={pose.left} />
            </g>
          </g>
        ) : null}
      </g>
    </svg>
  );
}
