import { cleanup } from "@testing-library/react";
import { afterEach } from "vitest";

afterEach(() => cleanup());

// jsdom has no object URLs; images just get a stable fake.
if (!URL.createObjectURL) URL.createObjectURL = () => "blob:test";
