import { mount } from "svelte";
import App from "./App.svelte";
import "./app.css";
import { installKeyboard } from "./lib/keyboard";

installKeyboard();

const app = mount(App, { target: document.getElementById("app")! });

export default app;
