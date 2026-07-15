import asyncio

import unio


async def main() -> None:
    async with unio.Agent(unio.Codex, cwd=".") as agent:
        session = agent.new_session()
        result = await session.run("Apply the requested change")
        while result.blocked is not None:
            if result.blocked.options:
                for option in result.blocked.options:
                    print(f"{option.value}: {option.label}")
                prompt = "Choose an option value: "
            else:
                prompt = f"{result.blocked.message}\nReply: "
            selected = await asyncio.to_thread(input, prompt)
            result = await session.continue_(selected)
        print(result.text)


if __name__ == "__main__":
    asyncio.run(main())
