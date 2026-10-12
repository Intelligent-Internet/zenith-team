export type ChatCompanyModeOption = {
  description: string;
  footer: string;
  icon: "leaf" | "crown";
  id: "light" | "full";
  label: string;
  tags: string[];
};

export const chatCompanyModeOptions = [
  {
    id: "light",
    icon: "leaf",
    label: "Light Mode",
    description: "Focus on core execution with essentials to ship your product",
    tags: ["Product", "Engineering", "Marketing", "Operations Lite"],
    footer: "Faster start. Less overhead. Perfect for early stage.",
  },
  {
    id: "full",
    icon: "crown",
    label: "Company Full Mode",
    description: "Complete company systems across every area of the business",
    tags: [
      "Product",
      "Engineering",
      "Marketing",
      "Finance",
      "Customers",
      "Operations",
    ],
    footer: "Maximum clarity. Full leverage. Build for sustainable scale.",
  },
] satisfies ChatCompanyModeOption[];
