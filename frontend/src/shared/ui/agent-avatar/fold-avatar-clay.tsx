import { useId } from "react";
import clayAtlas from "./assets/fold-clay-atlas.png";
import { FoldSecretaryAccessory } from "./fold-secretary-accessory";

type Palette = { color: string; light: string; dark: string; shadow: string };

// Each viewBox selects one untouched part from the generated puppet atlas.
const parts = {
  body: [50, 22, 466, 454],
  leftArm: [634, 147, 186, 250],
  rightArm: [1039, 132, 227, 244],
  ankle: [245, 330, 125, 120],
  leftFoot: [1438, 222, 254, 139],
  rightFoot: [119, 609, 265, 143],
  leftEye: [576, 515, 269, 312],
  rightEye: [1012, 529, 251, 292],
  pupil: [1506, 583, 126, 177],
} as const;

// The generated atlas has an opaque backdrop. Silhouette clips make each
// textured part transparent in the renderer without changing the source image.
const silhouettes = {
  body: "M347 30C362 28 375 33 384 39C412 55 442 78 467 101C493 129 506 149 502 181C497 207 494 234 495 260C496 294 502 335 500 370C500 403 489 428 465 438C431 455 374 462 322 465C264 468 194 470 147 466C116 464 100 453 92 442C77 425 71 403 69 380C67 355 70 335 69 310C67 285 64 256 63 230C59 206 59 182 60 162C61 140 71 118 85 106C104 89 148 73 189 64C237 51 285 40 325 32C333 30 340 29 347 30Z",
  leftArm:
    "M775 155C800 153 811 174 811 198C811 222 801 244 797 265C811 282 816 302 813 324C809 365 782 389 747 392C709 394 675 372 657 345C639 320 642 290 650 266C666 222 706 183 743 163C755 158 765 155 775 155Z",
  rightArm:
    "M1200 141C1227 137 1250 152 1258 177C1269 211 1248 247 1227 269C1212 302 1176 332 1145 351C1105 378 1077 373 1059 349C1040 323 1044 293 1058 273C1076 249 1100 242 1122 228C1133 204 1129 184 1147 162C1161 146 1181 141 1200 141Z",
  // A clean patch of the body supplies matching clay for the short ankle.
  ankle: "M259 332H356L363 423Q363 447 310 447Q249 447 249 429Z",
  leftFoot:
    "M1451 284C1462 259 1493 243 1525 235C1568 225 1616 232 1648 248C1675 262 1687 284 1684 309C1681 330 1663 343 1637 348C1607 354 1525 354 1488 346C1453 339 1444 315 1451 284Z",
  rightFoot:
    "M130 665C148 637 188 620 227 617C274 612 325 622 352 645C374 663 381 687 375 710C370 729 354 738 330 741C284 748 200 745 161 735C130 728 120 700 130 665Z",
  leftEye:
    "M702 524C751 521 791 550 814 594C838 640 843 699 825 744C808 787 770 815 725 818C679 823 642 805 615 771C588 736 580 690 587 647C594 602 613 564 645 541C662 530 681 525 702 524Z",
  rightEye:
    "M1134 537C1173 535 1209 558 1231 595C1253 633 1260 683 1249 727C1238 773 1207 805 1164 811C1122 819 1086 802 1060 769C1030 732 1018 688 1023 648C1026 608 1044 574 1074 553C1091 542 1112 537 1134 537Z",
  pupil:
    "M1575 592C1603 590 1621 620 1623 654C1628 691 1616 728 1595 744C1573 759 1548 748 1531 726C1514 704 1509 672 1518 642C1527 611 1550 591 1575 592Z",
} as const;

function ClayPart({
  part,
  x,
  y,
  width,
  height,
}: {
  part: keyof typeof parts;
  x: number;
  y: number;
  width: number;
  height: number;
}) {
  const clipId = useId().replace(/:/g, "");
  return (
    <svg
      x={x}
      y={y}
      width={width}
      height={height}
      viewBox={parts[part].join(" ")}
      preserveAspectRatio="none"
      overflow="hidden"
    >
      <defs>
        <clipPath id={clipId}>
          <path d={silhouettes[part]} />
        </clipPath>
      </defs>
      <image
        href={clayAtlas}
        width="1774"
        height="887"
        clipPath={`url(#${clipId})`}
      />
    </svg>
  );
}

function colorChannels(hex: string) {
  return [1, 3, 5].map(
    (offset) => parseInt(hex.slice(offset, offset + 2), 16) / 255,
  );
}

export function FoldAvatarClay({
  id,
  palette,
  originalColor,
  replayKey,
  accessory,
}: {
  id: string;
  palette: Palette;
  originalColor: boolean;
  replayKey: number;
  accessory?: "secretary";
}) {
  const paint = (name: string) => `url(#${id}-${name})`;
  const recolor = originalColor ? undefined : paint("clay-color");
  const tones = [
    "#23170f",
    palette.shadow,
    palette.dark,
    palette.color,
    palette.light,
    "#fff8ec",
  ].map(colorChannels);

  return (
    <svg
      key={replayKey}
      className="fold-avatar__svg"
      viewBox="0 0 240 240"
      fill="none"
      aria-hidden="true"
      focusable="false"
    >
      <defs>
        <filter
          id={`${id}-clay-color`}
          x="-3%"
          y="-3%"
          width="106%"
          height="106%"
          colorInterpolationFilters="sRGB"
        >
          <feColorMatrix type="saturate" values="0" />
          <feComponentTransfer>
            <feFuncR
              type="table"
              tableValues={tones.map((tone) => tone[0]).join(" ")}
            />
            <feFuncG
              type="table"
              tableValues={tones.map((tone) => tone[1]).join(" ")}
            />
            <feFuncB
              type="table"
              tableValues={tones.map((tone) => tone[2]).join(" ")}
            />
          </feComponentTransfer>
        </filter>
        <radialGradient id={`${id}-ground`}>
          <stop stopColor="#52402c" stopOpacity=".16" />
          <stop offset=".45" stopColor="#52402c" stopOpacity=".08" />
          <stop offset="1" stopColor="#52402c" stopOpacity="0" />
        </radialGradient>
        <radialGradient id={`${id}-foot-contact`}>
          <stop stopColor="#48301c" stopOpacity=".48" />
          <stop offset=".45" stopColor="#48301c" stopOpacity=".25" />
          <stop offset="1" stopColor="#48301c" stopOpacity="0" />
        </radialGradient>
        <linearGradient id={`${id}-ankle-shade`} x2="0" y2="1">
          <stop stopColor="#542711" stopOpacity=".48" />
          <stop offset="1" stopColor="#542711" stopOpacity="0" />
        </linearGradient>
        <filter
          id={`${id}-eye-contact`}
          x="-25%"
          y="-20%"
          width="150%"
          height="150%"
          colorInterpolationFilters="sRGB"
        >
          <feDropShadow
            dx="-.45"
            dy="1.05"
            stdDeviation=".7"
            floodColor="#4a2b19"
            floodOpacity=".42"
          />
        </filter>
        <filter
          id={`${id}-mouth-contact`}
          x="-25%"
          y="-40%"
          width="150%"
          height="180%"
          colorInterpolationFilters="sRGB"
        >
          <feDropShadow
            dx="0"
            dy=".6"
            stdDeviation=".4"
            floodColor="#4a2b19"
            floodOpacity=".3"
          />
        </filter>
        <clipPath id={`${id}-left-gaze`}>
          <ellipse cx="99" cy="118" rx="20.6" ry="26.3" />
        </clipPath>
        <clipPath id={`${id}-right-gaze`}>
          <ellipse cx="154" cy="112" rx="18.5" ry="24.5" />
        </clipPath>
      </defs>

      <g className="fold-avatar__crop">
        <g className="fold-avatar__shadow">
          <ellipse cx="124" cy="215" rx="70" ry="9" fill={paint("ground")} />
          <ellipse
            cx="85"
            cy="215"
            rx="22"
            ry="3.2"
            fill={paint("foot-contact")}
          />
          <ellipse
            cx="165"
            cy="215"
            rx="23"
            ry="3.2"
            fill={paint("foot-contact")}
          />
        </g>
        <g className="fold-avatar__reaction">
          <g className="fold-avatar__character">
            <g className="fold-avatar__feet" filter={recolor}>
              <g className="fold-avatar__foot fold-avatar__foot--left">
                <ClayPart part="ankle" x={79} y={187} width={23} height={22} />
                <path d="M82 194H99L100 204H81Z" fill={paint("ankle-shade")} />
                <ClayPart
                  part="leftFoot"
                  x={65}
                  y={192.5}
                  width={39}
                  height={24.5}
                />
              </g>
              <g className="fold-avatar__foot fold-avatar__foot--right">
                <ClayPart part="ankle" x={145} y={184} width={25} height={24} />
                <path
                  d="M148 191H167L168 202H147Z"
                  fill={paint("ankle-shade")}
                />
                <ClayPart
                  part="rightFoot"
                  x={145}
                  y={190.5}
                  width={40}
                  height={25.5}
                />
              </g>
            </g>
            <g
              className="fold-avatar__arm fold-avatar__arm--left"
              filter={recolor}
            >
              <ClayPart part="leftArm" x={34} y={145} width={28} height={40} />
            </g>
            <g
              className="fold-avatar__arm fold-avatar__arm--right"
              filter={recolor}
            >
              <ClayPart
                part="rightArm"
                x={192}
                y={110}
                width={33}
                height={37}
              />
            </g>
            <g className="fold-avatar__body">
              <g filter={recolor}>
                <ClayPart part="body" x={42} y={36} width={160} height={166} />
              </g>
              <g className="fold-avatar__face">
                <g className="fold-avatar__open-eyes">
                  <g className="fold-avatar__eye fold-avatar__eye--left">
                    <g filter={paint("eye-contact")}>
                      <ClayPart
                        part="leftEye"
                        x={77}
                        y={90.5}
                        width={44}
                        height={55}
                      />
                    </g>
                    <g clipPath={paint("left-gaze")}>
                      <g className="fold-avatar__gaze">
                        <ClayPart
                          part="pupil"
                          x={93}
                          y={108.5}
                          width={13.5}
                          height={20}
                        />
                      </g>
                    </g>
                  </g>
                  <g className="fold-avatar__eye fold-avatar__eye--right">
                    <g filter={paint("eye-contact")}>
                      <ClayPart
                        part="rightEye"
                        x={134}
                        y={85.5}
                        width={40}
                        height={53}
                      />
                    </g>
                    <g clipPath={paint("right-gaze")}>
                      <g className="fold-avatar__gaze">
                        <ClayPart
                          part="pupil"
                          x={147}
                          y={102}
                          width={13}
                          height={19.5}
                        />
                      </g>
                    </g>
                  </g>
                </g>
                <g
                  className="fold-avatar__closed-eyes"
                  stroke={palette.shadow}
                  strokeWidth="2.8"
                  strokeLinecap="round"
                >
                  <path d="M84 120Q97 129 111 117" />
                  <path d="M141 114Q153 122 167 111" />
                </g>
                <g
                  className="fold-avatar__happy-eyes"
                  stroke={palette.shadow}
                  strokeWidth="2.8"
                  strokeLinecap="round"
                >
                  <path d="M85 119Q98 107 111 116" />
                  <path d="M142 113Q154 101 168 110" />
                </g>
                <g
                  className="fold-avatar__brows"
                  stroke={palette.shadow}
                  strokeWidth="2"
                  strokeLinecap="round"
                  opacity=".72"
                >
                  <path d="M85 85Q93 87 102 81" />
                  <path d="M145 77Q156 79 162 83" />
                </g>
                <g
                  filter={paint("mouth-contact")}
                  stroke="#513321"
                  strokeLinecap="round"
                  strokeWidth="2.8"
                >
                  <path
                    className="fold-avatar__mouth fold-avatar__mouth--smile"
                    d="M121 144Q128 154 136 142"
                  />
                  <path
                    className="fold-avatar__mouth fold-avatar__mouth--thinking"
                    d="M128 149Q133 147 138 148"
                  />
                  <ellipse
                    className="fold-avatar__mouth fold-avatar__mouth--waiting"
                    cx="129"
                    cy="146"
                    rx="3.3"
                    ry="4.5"
                    fill="#513321"
                    stroke="none"
                  />
                  <path
                    className="fold-avatar__mouth fold-avatar__mouth--error"
                    d="M121 151Q128 143 136 148"
                  />
                  <path
                    className="fold-avatar__mouth fold-avatar__mouth--done"
                    d="M119 143Q129 158 139 140"
                  />
                </g>
                {accessory === "secretary" && (
                  <FoldSecretaryAccessory part="glasses" />
                )}
              </g>
              {accessory === "secretary" && (
                <FoldSecretaryAccessory part="scarf" />
              )}
            </g>
          </g>
        </g>
      </g>
    </svg>
  );
}
