import asyncio

import unio


async def main() -> None:
    async with unio.Agent(unio.Codex, cwd=".") as agent:
        result = await agent.new_session().run(unio.UserMessage("Explain this repository briefly"))
        print(result.text)


if __name__ == "__main__":
    asyncio.run(main())
