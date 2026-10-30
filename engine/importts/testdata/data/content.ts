import React from "react";
import { BatchAnalyzerDescription } from "@/components/descriptions/BatchAnalyzerDescription";
import type { Category } from "./taxonomy";

export function getCategoryIcon(category: Category): string {
  const iconMap: Record<Category, string> = { Engineering: "💻", Games: "🎮", "Social Tools": "👥", Art: "🎨" };
  return iconMap[category] || "📄";
}

export const NICKEL_SYSTEM = {
  version: "1.0.0",
  content: [
    // -----------------------------------------------------------------------
    // Section header
    // -----------------------------------------------------------------------
    {
      id: "world",
      hide: false,
      title: "My World",
      description: "A 3D world.",
      url: "https://nicolebelovoskey.com",
      category: "Engineering",
      date: "2025-01",
      thumbnail: "/project-screenshots/world.png",
      tech: ["TypeScript", "C#"],
      source: "https://github.com/test/world",
      surfaces: ["desktop", "software", "design"],
      softwareOverride: { title: "World (software)", description: "Software words.", tech: ["Next.js"] },
    },
    {
      id: "art-room",
      hide: false,
      title: "Art Room",
      description: "A room.",
      url: "https://nicolebelovoskey.com/holodeck/art",
      category: "Art",
      surfaces: ["desktop", "software"],
    },
    {
      // TODO: the link and the previews point at different domains
      id: "videogamequest",
      hide: false,
      title: "RPG Quests",
      description: "Quests.",
      url: "https://rpg-quests.netlify.app/",
      category: "AI / Productivity",
      date: "Sometime in like Nov 2024",
      surfaces: ["software"],
      portfolioOnly: { preview: { mobileScreens: ["https://videogamequest.me/", "https://videogamequest.me/demo"] } },
    },
    {
      id: "toy",
      hide: false,
      title: "Toy",
      description: "The lightweight one.",
      route: "/toy",
      category: "Games",
      surfaces: ["desktop", "software"],
      desktopOnly: { windowWidth: 900, windowHeight: 700, contentfulDescription: BatchAnalyzerDescription },
    },
    {
      id: "big",
      hide: true,
      title: "Big",
      description: "The real one.",
      route: "#",
      category: "Social App",
      status: "in-development",
      surfaces: ["desktop", "software"],
    },
    {
      id: "ext",
      hide: false,
      title: "External",
      description: "Elsewhere.",
      url: "https://example.com/app",
      category: "Social Tools",
      surfaces: ["software"],
      desktopOnly: { openInNewTab: false },
    },
    {
      id: "art-paintings",
      hide: false,
      title: "Paintings",
      description: "Paint.",
      route: "/art-gallery/paintings",
      category: "Art",
      mediums: ["Digital"],
      surfaces: ["desktop", "art"],
      portfolioOnly: { artGrouping: "collection" },
      artOverride: { description: "Paintings, for the art page." },
    },
    { id: "about", hide: false, title: "About", description: "x", route: "/about", category: "Engineering", surfaces: ["desktop"] },
  ],
  featured: ["toy"],
};
