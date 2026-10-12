import { useId } from "react";

/** Authored character wardrobe, not action/navigation iconography. */
export function FoldSecretaryAccessory({
  part,
}: {
  part: "glasses" | "scarf";
}) {
  const id = useId().replace(/:/g, "");
  if (part === "glasses") {
    return (
      <g stroke="#365861" strokeWidth="3.5" strokeLinecap="round">
        <ellipse cx="99" cy="119" rx="23" ry="27" fill="none" />
        <ellipse cx="154" cy="113" rx="21" ry="26" fill="none" />
        <path d="M122 116Q127 111 133 115M76 115L67 111M175 108L183 104" />
        <path
          d="M84 105Q87 99 93 98M140 101Q143 95 148 94"
          stroke="#E5F4EB"
          strokeWidth="2"
          opacity=".65"
        />
      </g>
    );
  }
  return (
    <g>
      <defs>
        <linearGradient
          id={`${id}-silk`}
          x1="112"
          y1="168"
          x2="137"
          y2="197"
          gradientUnits="userSpaceOnUse"
        >
          <stop stopColor="#FFD3BD" />
          <stop offset=".5" stopColor="#ED817B" />
          <stop offset="1" stopColor="#B65059" />
        </linearGradient>
      </defs>
      <path
        d="M86 168Q121 181 157 164L143 185L122 178L107 190Z"
        fill="#FFF2DE"
      />
      <path
        d="M119 177L126 174L139 194L127 191L122 197L117 183Z"
        fill={`url(#${id}-silk)`}
      />
      <path d="M115 175Q121 171 128 175L126 182L119 183Z" fill="#F5A292" />
    </g>
  );
}
